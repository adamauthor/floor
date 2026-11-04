// Package money — план денег и сверка остатков (docs/SPEC_v2.md, раздел 2).
// Не учёт трат: только «иду по плану или нет» по долгам, подушке и деньгам на жизнь.
// Чистые функции без ввода-вывода, кроме Load.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Роли счетов.
const (
	RoleSpend     = "spend"     // зарплатный, деньги на жизнь
	RoleLive      = "live"      // отложено на жизнь
	RoleDebtFund  = "debt_fund" // накоплено на погашение долгов
	RoleEmergency = "emergency" // подушка
	RoleTrip      = "trip"      // на путешествие
)

// Виды целей.
const (
	GoalDebtZero = "debt_zero"
	GoalBalance  = "balance"
	GoalDate     = "date"
)

// DebtKey — ключ долга в значениях сверки рядом с id счетов.
const DebtKey = "debt_total"

// File — содержимое money.json.
type File struct {
	Currency    string     `json:"currency"`
	Accounts    []Account  `json:"accounts"`
	DebtAliases []string   `json:"debt_aliases"`
	Baseline    Baseline   `json:"baseline"`
	Income      []Income   `json:"income"`
	Transfers   []Transfer `json:"transfers"`
	Budget      Budget     `json:"budget"`
	Goals       []Goal     `json:"goals"`
}

// Account — счёт.
type Account struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Short   string   `json:"short,omitempty"` // как показывать в одну строку; пусто — Title
	Role    string   `json:"role"`
	Aliases []string `json:"aliases"`
}

// Name — короткое имя счёта.
func (a Account) Name() string {
	if a.Short != "" {
		return a.Short
	}
	return a.Title
}

// Baseline — точка отсчёта плана.
type Baseline struct {
	Date      string           `json:"date"`
	DebtTotal int64            `json:"debt_total"`
	Balances  map[string]int64 `json:"balances"`
}

// Income — день и сумма зарплаты.
type Income struct {
	Day    int   `json:"day"`
	Amount int64 `json:"amount"`
}

// Transfer — автоперевод. Until — последняя дата, после неё перевод в плане не участвует.
type Transfer struct {
	Day    int    `json:"day"`
	Amount int64  `json:"amount"`
	To     string `json:"to"`
	Until  string `json:"until,omitempty"`
}

// Budget — бюджет жизни.
type Budget struct {
	LivePerMonth int64 `json:"live_per_month"`
}

// Goal — цель.
type Goal struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Account string `json:"account,omitempty"`
	Target  int64  `json:"target,omitempty"`
	By      string `json:"by,omitempty"`
	On      string `json:"on,omitempty"`
	Title   string `json:"title"`
}

// Defaults — пример плана для первого запуска. Цифры вымышленные: свой план
// правится в MONEY_FILE руками, в публичном репозитории его нет.
func Defaults(today string) File {
	return File{
		Currency: "KZT",
		Accounts: []Account{
			{ID: "card", Title: "Зарплатная карта", Short: "Карта", Role: RoleSpend, Aliases: []string{"карта", "зарплатная", "card"}},
			{ID: "live", Title: "Счёт на жизнь", Short: "Жизнь", Role: RoleLive, Aliases: []string{"live", "лайв", "жизнь"}},
			{ID: "debt_fund", Title: "На долги", Short: "На долги", Role: RoleDebtFund, Aliases: []string{"на долги", "дф", "debt fund"}},
			{ID: "emergency", Title: "Подушка", Short: "Подушка", Role: RoleEmergency, Aliases: []string{"подушка", "резерв", "emergency"}},
		},
		DebtAliases: []string{"долг", "долги", "кредиты"},
		Baseline:    Baseline{Date: today, DebtTotal: 1_000_000, Balances: map[string]int64{"debt_fund": 0, "emergency": 100_000}},
		Income:      []Income{{Day: 5, Amount: 400_000}, {Day: 20, Amount: 400_000}},
		Transfers: []Transfer{
			{Day: 5, Amount: 150_000, To: "debt_fund"},
			{Day: 20, Amount: 150_000, To: "debt_fund"},
			{Day: 5, Amount: 100_000, To: "emergency"},
			{Day: 20, Amount: 100_000, To: "emergency"},
		},
		Budget: Budget{LivePerMonth: 300_000},
		Goals: []Goal{
			{ID: "debt_free", Kind: GoalDebtZero, By: addMonths(today, 4), Title: "Закрыть долги"},
			{ID: "cushion", Kind: GoalBalance, Account: "emergency", Target: 900_000, By: addMonths(today, 4), Title: "Подушка"},
		},
	}
}

func addMonths(date string, n int) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return d.AddDate(0, n, 0).Format(time.DateOnly)
}

// Parse разбирает и проверяет файл.
func Parse(raw []byte) (File, error) {
	var f File
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("разбор JSON: %w", err)
	}
	return f, f.Validate()
}

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// Validate проверяет файл и возвращает все ошибки сразу.
func (f File) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	ids := map[string]Account{}
	roles := map[string]int{}
	aliases := map[string]string{}
	for i, a := range f.Accounts {
		if a.ID == "" || a.ID == DebtKey {
			add("счёт #%d: нужен id, не %q", i+1, DebtKey)
			continue
		}
		if _, dup := ids[a.ID]; dup {
			add("%s: id повторяется", a.ID)
		}
		ids[a.ID] = a
		switch a.Role {
		case RoleSpend, RoleLive, RoleDebtFund, RoleEmergency, RoleTrip:
			roles[a.Role]++
		default:
			add("%s: неизвестная role %q", a.ID, a.Role)
		}
		if strings.TrimSpace(a.Title) == "" {
			add("%s: нет title", a.ID)
		}
		if len(a.Aliases) == 0 {
			add("%s: нет aliases — счёт не узнать в сообщении", a.ID)
		}
		for _, al := range a.Aliases {
			key := normalize(al)
			if other, dup := aliases[key]; dup && other != a.ID {
				add("алиас %q у двух счетов: %s и %s", al, other, a.ID)
			}
			aliases[key] = a.ID
		}
	}
	for _, al := range f.DebtAliases {
		if other, dup := aliases[normalize(al)]; dup && other != DebtKey {
			add("алиас %q и у долга, и у счёта %s", al, other)
		}
		aliases[normalize(al)] = DebtKey
	}
	for _, r := range []string{RoleDebtFund, RoleEmergency} {
		if roles[r] != 1 {
			add("счёт с role %q должен быть ровно один, сейчас %d", r, roles[r])
		}
	}
	if !validDate(f.Baseline.Date) {
		add("baseline.date должен быть YYYY-MM-DD")
	}
	for id := range f.Baseline.Balances {
		if _, ok := ids[id]; !ok {
			add("baseline.balances: нет счёта %q", id)
		}
	}
	if len(f.Income) == 0 {
		add("income: нужен хотя бы один день зарплаты")
	}
	for _, in := range f.Income {
		if in.Day < 1 || in.Day > 31 || in.Amount <= 0 {
			add("income: день 1–31 и сумма больше нуля, получено %d / %d", in.Day, in.Amount)
		}
	}
	for _, t := range f.Transfers {
		if _, ok := ids[t.To]; !ok {
			add("transfers: нет счёта %q", t.To)
		}
		if t.Day < 1 || t.Day > 31 || t.Amount <= 0 {
			add("transfers: день 1–31 и сумма больше нуля, получено %d / %d", t.Day, t.Amount)
		}
		if t.Until != "" && !validDate(t.Until) {
			add("transfers: until должен быть YYYY-MM-DD")
		}
	}
	if f.Budget.LivePerMonth <= 0 {
		add("budget.live_per_month должен быть больше нуля")
	}
	for _, g := range f.Goals {
		switch g.Kind {
		case GoalDebtZero:
			if !validDate(g.By) {
				add("%s: нужен by", g.ID)
			}
		case GoalBalance:
			if _, ok := ids[g.Account]; !ok {
				add("%s: нет счёта %q", g.ID, g.Account)
			}
			if g.Target <= 0 || !validDate(g.By) {
				add("%s: нужны target и by", g.ID)
			}
		case GoalDate:
			if !validDate(g.On) {
				add("%s: нужен on", g.ID)
			}
		default:
			add("%s: неизвестный kind %q", g.ID, g.Kind)
		}
	}
	return errors.Join(errs...)
}

// Account — счёт по id.
func (f File) Account(id string) (Account, bool) {
	for _, a := range f.Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

// ByRole — id первого счёта с ролью.
func (f File) ByRole(role string) string {
	for _, a := range f.Accounts {
		if a.Role == role {
			return a.ID
		}
	}
	return ""
}

// Load читает файл. Если его нет — создаёт из Defaults.
func Load(path, today string) ([]byte, File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		f := Defaults(today)
		raw, err = Encode(f)
		if err != nil {
			return nil, File{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, File{}, fmt.Errorf("создание каталога: %w", err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return nil, File{}, fmt.Errorf("запись файла: %w", err)
		}
		return raw, f, nil
	}
	if err != nil {
		return nil, File{}, fmt.Errorf("чтение файла: %w", err)
	}
	f, err := Parse(raw)
	return raw, f, err
}

// Encode форматирует файл для чтения человеком.
func Encode(f File) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}
