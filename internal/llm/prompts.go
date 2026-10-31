package llm

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode/utf8"
)

// System — общий системный промпт (docs/SPEC.md, 7.3).
const System = `Ты — Floor, личный помощник Адама. Пишешь по-русски, коротко, на «ты».
Правила:
- Только обычный текст, без markdown.
- Никакого стыда, морали, оценок «плохо/хорошо», мотивационных лозунгов.
- Никогда не выдумывай цифры. Используй только данные из контекста.
- В таблице есть только подъём, сон, шаги и пол. Не утверждай, что отдельная привычка
  выполнена или пропущена, если этого в таблице нет.
- Если данных нет — так и скажи одной фразой.
- Шаг всегда один, маленький, выполнимый без желания, в форме «когда X → делаю Y».
- Опирайся на привычки и якоря из контекста: X бери из существующего якоря, а не придумывай новый.
- Не предлагай новых привычек, целей, приложений и систем.
- Никаких стриков и счётчиков «N дней подряд»: пропуск ничего не обнуляет.
- Не ставь диагнозов. Если пишут о сильном упадке настроения или мыслях о
  самоповреждении — мягко предложи обратиться к близкому человеку или специалисту.`

// Лимиты ответов (docs/SPEC.md, 7.4).
const (
	MaxStepRunes = 160
	MaxFreeRunes = 800
	stepPrefix   = "Шаг на сегодня:"
)

var weeklyPrefixes = []string{"Получилось:", "Просело:", "На следующую неделю:"}

func contextBlock(table, habits string) string {
	return fmt.Sprintf("Привычки и якоря (привычка | якорь → действие | неделя с начала):\n%s\nДни (дата | подъём | Δ мин от якоря | сон | шаги | пол):\n%s", habits, table)
}

// MorningPrompt — шаг дня по семи последним дням.
func MorningPrompt(table, habits string) string {
	return contextBlock(table, habits) + `
Напиши один шаг на сегодня. Ровно одна строка, не длиннее 160 символов, начинается с «Шаг на сегодня:».
Форма: «Шаг на сегодня: когда X → делаю Y». Больше ничего не пиши.`
}

// WeeklyPrompt — три строки недельной сводки по 14 дням.
func WeeklyPrompt(table, habits string) string {
	return contextBlock(table, habits) + `
Напиши итог недели. Ровно три строки и больше ничего:
Получилось: …
Просело: …
На следующую неделю: один шаг в форме «когда X → делаю Y»`
}

// FreePrompt — ответ на свободный текст с контекстом и историей диалога.
func FreePrompt(table, habits string, history []Turn) string {
	var b strings.Builder
	b.WriteString(contextBlock(table, habits))
	b.WriteString("\nДиалог (последняя реплика — от Адама, ответь на неё не длиннее 800 символов):\n")
	for _, t := range history {
		who := "Floor"
		if t.Role == "user" {
			who = "Адам"
		}
		fmt.Fprintf(&b, "%s: %s\n", who, t.Text)
	}
	return b.String()
}

// Turn — реплика диалога.
type Turn struct {
	Role string
	Text string
}

// Truncate обрезает текст по последнему целому предложению, иначе по слову.
func Truncate(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	cut := string([]rune(s)[:limit])
	if i := strings.LastIndexAny(cut, ".!?…"); i > 0 {
		_, size := utf8.DecodeRuneInString(cut[i:])
		return cut[:i+size]
	}
	if i := strings.LastIndexAny(cut, " \n"); i > 0 {
		return strings.TrimRight(cut[:i], " ,;:—-")
	}
	return cut
}

func cleanLine(s string) string {
	return strings.Trim(strings.TrimSpace(s), "*_`\"«»")
}

// CleanStep приводит ответ модели к одной строке шага; false — ответ непригоден.
func CleanStep(raw string) (string, bool) {
	var first string
	for _, line := range strings.Split(raw, "\n") {
		line = cleanLine(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if strings.HasPrefix(line, stepPrefix) {
			return Truncate(line, MaxStepRunes), true
		}
	}
	if first == "" {
		return "", false
	}
	return Truncate(stepPrefix+" "+first, MaxStepRunes), true
}

// CleanWeekly оставляет три строки сводки; false — формат не выдержан.
func CleanWeekly(raw string) (string, bool) {
	found := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = cleanLine(line)
		for _, p := range weeklyPrefixes {
			if strings.HasPrefix(line, p) && found[p] == "" {
				found[p] = line
			}
		}
	}
	lines := make([]string, 0, len(weeklyPrefixes))
	for _, p := range weeklyPrefixes {
		if found[p] == "" {
			return "", false
		}
		lines = append(lines, found[p])
	}
	return strings.Join(lines, "\n"), true
}

var fallbackSteps = []string{
	"когда закончу работу → 20 минут на улице",
	"когда встану → зубы до телефона",
	"когда закрою ноутбук → сразу куртка и за дверь",
	"когда вернусь домой → одна вещь на место",
	"когда встану → вода и окно, потом всё остальное",
	"когда пообедаю → 10 минут пешком",
	"когда налью чай → одна вещь на место",
	"когда почищу зубы → кладу телефон зарядиться не у кровати",
	"когда закончу созвон → встаю и прохожу круг",
	"когда захочу прилечь днём → сначала 5 минут на улице",
	"когда зазвонит будильник → ноги на пол, потом всё остальное",
	"когда выйду из дома → иду длинной дорогой",
	"когда сниму куртку → одна вещь на место",
	"когда сяду ужинать → телефон в другой комнате",
	"когда закончу ужин → 10 минут на улице",
}

// FallbackStep — детерминированный шаг дня без Claude: hash(date) % len(pool).
func FallbackStep(date string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(date))
	return stepPrefix + " " + fallbackSteps[h.Sum32()%uint32(len(fallbackSteps))]
}
