package money

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plan — вымышленный план с той же формой, что в ТЗ: переводы до 5 февраля,
// подушка к 5 марта, путешествие раньше подушки.
func plan() File {
	f := Defaults("2026-11-04")
	f.Baseline = Baseline{Date: "2026-11-04", DebtTotal: 1_000_000, Balances: map[string]int64{"debt_fund": 100_000, "emergency": 50_000}}
	f.Income = []Income{{Day: 5, Amount: 500_000}, {Day: 18, Amount: 400_000}}
	f.Transfers = []Transfer{
		{Day: 5, Amount: 200_000, To: "debt_fund", Until: "2027-02-05"},
		{Day: 18, Amount: 200_000, To: "debt_fund", Until: "2027-02-05"},
		{Day: 5, Amount: 100_000, To: "emergency", Until: "2027-02-05"},
		{Day: 18, Amount: 50_000, To: "emergency", Until: "2027-02-05"},
	}
	f.Budget = Budget{LivePerMonth: 300_000}
	f.Goals = []Goal{
		{ID: "debt_free", Kind: GoalDebtZero, By: "2027-02-05", Title: "Закрыть долги"},
		{ID: "cushion", Kind: GoalBalance, Account: "emergency", Target: 1_000_000, By: "2027-03-05", Title: "Подушка"},
		{ID: "trip", Kind: GoalDate, On: "2027-02-23", Title: "Путешествие"},
	}
	return f
}

func TestDefaultsValidAndLoad(t *testing.T) {
	if err := Defaults("2026-11-04").Validate(); err != nil {
		t.Fatal(err)
	}
	if err := plan().Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x", "money.json")
	_, f, err := Load(path, "2026-11-04")
	if err != nil || len(f.Accounts) != 4 {
		t.Fatalf("создание: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("права %v", info.Mode().Perm())
	}
	if _, _, err := Load(path, "2026-12-01"); err != nil {
		t.Fatalf("чтение: %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path, "2026-12-01"); err == nil {
		t.Error("битый файл принят")
	}
}

func TestValidateRejects(t *testing.T) {
	for name, edit := range map[string]func(*File){
		"два emergency":    func(f *File) { f.Accounts[0].Role = RoleEmergency },
		"общий алиас":      func(f *File) { f.Accounts[1].Aliases = append(f.Accounts[1].Aliases, "КАРТА") },
		"алиас у долга":    func(f *File) { f.DebtAliases = append(f.DebtAliases, "подушка") },
		"перевод в никуда": func(f *File) { f.Transfers[0].To = "nope" },
		"плохая цель":      func(f *File) { f.Goals[1].Target = 0 },
		"плохой день":      func(f *File) { f.Income[0].Day = 32 },
		"нет бюджета":      func(f *File) { f.Budget.LivePerMonth = 0 },
		"роль":             func(f *File) { f.Accounts[0].Role = "x" },
		"baseline счёт":    func(f *File) { f.Baseline.Balances["nope"] = 1 },
		"until":            func(f *File) { f.Transfers[0].Until = "потом" },
		"вид цели":         func(f *File) { f.Goals[0].Kind = "x" },
	} {
		f := plan()
		edit(&f)
		if f.Validate() == nil {
			t.Errorf("%s: принято", name)
		}
	}
	raw, _ := Encode(plan())
	if _, err := Parse([]byte(strings.Replace(string(raw), `"currency"`, `"curency"`, 1))); err == nil {
		t.Error("опечатка в поле принята")
	}
}

func TestOccurrencesShortMonth(t *testing.T) {
	f := plan()
	f.Transfers = []Transfer{{Day: 31, Amount: 1, To: "emergency"}}
	got := f.Occurrences("emergency", "2027-01-31", "2027-04-30")
	want := []string{"2027-02-28", "2027-03-31", "2027-04-30"}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for i, o := range got {
		if o.Date != want[i] {
			t.Errorf("%d: %s, ожидалось %s", i, o.Date, want[i])
		}
	}
}

func TestCalc(t *testing.T) {
	f := plan()
	s := Merge("2026-12-20", map[string]int64{"card": 100_000, "live": 20_000, "debt_fund": 550_000, "emergency": 300_000, DebtKey: 700_000}, nil, f)
	r := f.Calc(s, nil)

	d := r.Debt
	if d.Net != 150_000 || d.Plan != 100_000 || d.Status != Lagging || d.ClosesOn != "2027-01-05" || d.Margin != 450_000 {
		t.Errorf("долги: %+v", d)
	}
	b := r.Balances[0]
	if b.Plan != 350_000 || b.AtBy != 550_000 || b.Shortfall != 450_000 || b.Status != OffTrack || b.Until != "2027-02-05" {
		t.Errorf("подушка: %+v", b)
	}
	l := r.Life
	if l.Available != 120_000 || l.Until != "2027-01-05" || l.Days != 16 || l.PerDay != 7_500 || l.BudgetDay != 9_868 || l.OK {
		t.Errorf("жизнь: %+v", l)
	}
	if r.Months != 1.0 || r.NetWorth != 270_000 || r.PrevWorth != nil || r.Worst() != OffTrack {
		t.Errorf("отчёт: %+v", r)
	}
	if len(r.Countdown) != 1 || r.Countdown[0].Days != 65 {
		t.Errorf("отсчёт: %+v", r.Countdown)
	}

	// По плану: всё ✅, долги закрываются раньше срока.
	ok := Merge("2026-12-20", map[string]int64{"card": 200_000, "live": 100_000, "debt_fund": 900_000, "emergency": 360_000, DebtKey: 1_000_000}, nil, f)
	r2 := f.Calc(ok, &s)
	if r2.Debt.Status != OnTrack || r2.Life.OK != true || *r2.PrevWorth != 270_000 {
		t.Errorf("по плану: %+v / %+v", r2.Debt, r2.Life)
	}
	// Долг закрыт.
	closed := Merge("2027-01-10", map[string]int64{"debt_fund": 400_000, DebtKey: 300_000}, &ok, f)
	if r3 := f.Calc(closed, nil); !r3.Debt.Closed || r3.Debt.Net != -100_000 {
		t.Errorf("закрыт: %+v", r3.Debt)
	}
}

func TestMergeCarriesFromPrevious(t *testing.T) {
	f := plan()
	prev := f.BaselineSnapshot()
	s := Merge("2026-11-20", map[string]int64{"emergency": 200_000}, &prev, f)
	if s.Balances["debt_fund"] != 100_000 || !s.Carried["debt_fund"] || s.Carried["emergency"] || *s.DebtTotal != 1_000_000 || !s.Carried[DebtKey] {
		t.Errorf("перенос: %+v", s)
	}
}

func TestNextIncome(t *testing.T) {
	f := plan()
	for date, want := range map[string]string{
		"2026-12-04": "2026-12-05", "2026-12-05": "2026-12-18", "2026-12-18": "2027-01-05", "2026-12-31": "2027-01-05",
	} {
		if got := f.NextIncome(date); got != want {
			t.Errorf("%s → %s, ожидалось %s", date, got, want)
		}
	}
}

func TestChecks(t *testing.T) {
	ws := plan().Checks()
	if len(ws) != 2 || ws[0].Kind != WarnAfterTransfers || ws[1].Kind != WarnTrip {
		t.Fatalf("предупреждения: %+v", ws)
	}
	if ws[0].AtBy != 600_000 || len(ws[0].Missing) != 2 || ws[0].Missing[0].Date != "2027-02-18" || ws[0].Missing[1].Date != "2027-03-05" {
		t.Errorf("подушка: %+v", ws[0])
	}
	f := plan()
	f.Budget.LivePerMonth = 500_000
	f.Accounts = append(f.Accounts, Account{ID: "trip", Title: "Путешествие", Role: RoleTrip, Aliases: []string{"трип"}})
	ws = f.Checks()
	if len(ws) != 2 || ws[1].Kind != WarnOverspend || ws[1].Gap != 150_000 {
		t.Errorf("с бюджетом 500к: %+v", ws)
	}
}

func TestParseText(t *testing.T) {
	f := plan()
	p := f.ParseText("Карта 142к, лайв 35 000, дф 610к, подушка 900к, долг 1 120 000 ₸")
	want := map[string]int64{"card": 142_000, "live": 35_000, "debt_fund": 610_000, "emergency": 900_000, DebtKey: 1_120_000}
	if !p.IsReconcile() || p.NeedsHelp() || len(p.Values) != len(want) {
		t.Fatalf("разбор: %+v", p)
	}
	for k, v := range want {
		if p.Values[k] != v {
			t.Errorf("%s = %d, ожидалось %d", k, p.Values[k], v)
		}
	}

	for text, v := range map[string]int64{
		"подушка 142 к":        142_000,
		"подушка 142k":         142_000,
		"подушка 142.000":      142_000,
		"подушка 1,12м":        1_120_000,
		"подушка 1.12 млн":     1_120_000,
		"подушка: 1 120 000 ₸": 1_120_000,
		"подушка 900 тыс":      900_000,
		"подушка 1.125 млн":    1_125_000,
	} {
		if got := f.ParseText(text).Values["emergency"]; got != v {
			t.Errorf("%q → %d, ожидалось %d", text, got, v)
		}
	}

	if p := f.ParseText("купил кофе за 1500"); p.IsReconcile() {
		t.Errorf("кофе — не сверка: %+v", p)
	}
	if p := f.ParseText("подушка выросла до 900к"); p.IsReconcile() || !p.NeedsHelp() {
		t.Errorf("один счёт — не сверка: %+v", p)
	}
	if p := f.ParseText("💰 подушка 900к"); !p.IsReconcile() || !p.NeedsHelp() {
		t.Errorf("явная сверка с одним числом: %+v", p)
	}
	if p := f.ParseText("остатки: карта и лайв по 100к, долги 500к"); !p.Ambiguous || !p.NeedsHelp() {
		t.Errorf("одно число на два счёта: %+v", p)
	}
	if p := f.ParseText("на долги 500к, долги 900к"); p.Values["debt_fund"] != 500_000 || p.Values[DebtKey] != 900_000 {
		t.Errorf("длинный алиас первым: %+v", p.Values)
	}
	if p := f.ParseText("карта 10 минут, подушка 5м"); p.Values["card"] != 10 || p.Values["emergency"] != 5_000_000 {
		t.Errorf("«м» внутри слова: %+v", p.Values)
	}
}

func TestLongNumbersIgnored(t *testing.T) {
	f := plan()
	p := f.ParseText("карта 4400 4301 1234 5678 142к, подушка 123456789012345 900к")
	if p.Values["card"] != 142_000 || p.Values["emergency"] != 900_000 {
		t.Errorf("номера карт не отброшены: %+v", p.Values)
	}
	if s := Scrub("карта 4400430112345678"); strings.Contains(s, "4400") {
		t.Errorf("Scrub: %q", s)
	}
	if f.Valid(map[string]int64{"card": 123456789012}) || f.Valid(map[string]int64{"x": 1}) || f.Valid(map[string]int64{"card": -1}) || !f.Valid(map[string]int64{"card": 1}) {
		t.Error("Valid")
	}
	if len(f.Keys()) != 5 {
		t.Error("Keys")
	}
}

func TestTransfersAndStatus(t *testing.T) {
	f := plan()
	if f.TransfersEnd() != "2027-02-05" || f.MonthlyTransfers("debt_fund", "2026-12-01") != 400_000 || f.MonthlyTransfers("", "2027-03-01") != 0 {
		t.Error("переводы")
	}
	if OnTrack.Mark() != "✅" || OffTrack.Word() != "off_track" {
		t.Error("статусы")
	}
	if a, ok := f.Account("live"); !ok || a.Name() != "Жизнь" {
		t.Error("Account")
	}
}
