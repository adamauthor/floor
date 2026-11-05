package llm

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"unicode/utf8"
)

// System — общий системный промпт (docs/SPEC.md, 7.3).
const System = `Ты — Floor, личный помощник Адама. Пишешь по-русски, коротко, на «ты».
Правила:
- Только обычный текст, без markdown.
- Никакого стыда, морали, оценок «плохо/хорошо», мотивационных лозунгов.
- Никогда не выдумывай цифры. Используй только данные из контекста.
- В таблице есть подъём, сон, шаги, пол дня и отметки привычек (цель, пол или мимо).
  Не утверждай, что привычка выполнена или пропущена, если в таблице её отметки нет.
- У каждой привычки есть цель и пол — минимум на плохой день. После пропуска опирайся на пол.
- Если данных нет — так и скажи одной фразой.
- Шаг всегда один, маленький, выполнимый без желания, в форме «когда X → делаю Y».
- Опирайся на привычки и якоря из контекста: X бери из существующего якоря, а не придумывай новый.
- Сам не предлагай новых привычек, целей, приложений и систем. Если Адам просит — помогай.
- Никаких стриков и счётчиков «N дней подряд»: пропуск ничего не обнуляет.
- В контексте могут быть блоки «Деньги», «Тренировки», «Тема недели». Цифры бери только оттуда.
- О деньгах: план и цели Адама уже решены. Не предлагай новых целей и продуктов,
  не советуй конкретные акции, монеты или фонды, не обещай доходность.
- Тренировка — силовая, бокс, единоборства, HIIT и похожее. Прогулки и бег тренировкой не считаются.
- Никогда не пиши «сохранил», «зафиксировал», «запомнил», «записал»: сохраняет только код,
  и только через блок ДЕЙСТВИЯ. Без него ничего не сохраняется.
- Не ставь диагнозов. Если пишут о сильном упадке настроения или мыслях о
  самоповреждении — мягко предложи обратиться к близкому человеку или специалисту.`

// Лимиты ответов (docs/SPEC.md, 7.4).
const (
	MaxStepRunes = 160
	MaxFreeRunes = 800
	stepPrefix   = "Шаг на сегодня:"
)

var weeklyPrefixes = []string{"Получилось:", "Узкое место:", "На следующую неделю:"}

// bottleneckPrefix — последняя строка отчёта о деньгах.
const bottleneckPrefix = "Узкое место:"

func contextBlock(table, habits string) string {
	return fmt.Sprintf("Привычки и якоря (привычка | якорь → цель | пол | неделя с начала):\n%s\nДни (дата | подъём | Δ мин от якоря | сон | шаги | пол дня | привычки):\n%s", habits, table)
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
Узкое место: одно ограничение по всем данным недели (сон, шаги, привычки, тренировки, деньги), которое сильнее всего тормозит остальное
На следующую неделю: один шаг именно по узкому месту, в форме «когда X → делаю Y»`
}

// Commands — что Claude может поменять по просьбе Адама и в каком виде.
const Commands = `Если Адам просит что-то изменить, отметить, запомнить или показать график — после ответа последней строкой
напиши «ДЕЙСТВИЯ:» и JSON-массив. Код покажет изменения и применит их только после «Да», поэтому
в ответе не пиши, что уже сделано: напиши, что предлагаешь. Если ни о чём таком не просили —
блока нет. Не выдумывай: id и значения бери из блока «Что можно менять», даты — YYYY-MM-DD.
Действия (поля, кроме op, — только нужные):
{"op":"habit_add","id":"латиницей","emoji":"🚶","title":"…","anchor":"когда X","goal":"полная версия","floor":"минимум","queued":false,"since":"дата старта"}
  тренировка: "kind":"workout","per_week":3
{"op":"habit_edit","id":"…","title"|"emoji"|"anchor"|"goal"|"floor"|"per_week":…}
{"op":"habit_pause"|"habit_resume"|"habit_queue"|"habit_delete","id":"…"}
{"op":"settings","wake_anchor":"HH:MM","tolerance":60,"steps_floor":7000,"steps_goal":10000,"evening_at":"HH:MM","weekly_at":"HH:MM"}
{"op":"mark","date":"…","marks":{"id":"floor"|"goal"|"miss","*":"floor"}}  — отметка привычек за день; "*" — все остальные
{"op":"workout","date":"…"}  — была тренировка без часов
{"op":"money_transfers","to":"id счёта","until":"дата"}  — до какой даты идут автопереводы на счёт
{"op":"money_goal","id":"…","target":1950000,"by":"дата","on":"дата"}
{"op":"money_budget","live_per_month":350000}
{"op":"remember","text":"факт коротко, с датами и суммами"}  — планы, договорённости, важные факты на будущее
{"op":"forget","note":3}
{"op":"chart","what":"week"|"month"|"money","date":"…"}  — показать график: неделя или месяц, куда входит дата (по умолчанию сегодня),
  или план денег. Графики рисует код по базе и отправляет сразу, без «Да». Сам не рисуй таблицы и псевдографику.
Остатки по счетам Адам присылает отдельным сообщением: их разбирает код, сюда не пиши.`

// FreePrompt — ответ на свободный текст с контекстом и историей диалога.
// control — что можно менять (привычки с id, настройки, деньги, заметки).
func FreePrompt(table, habits, control string, history []Turn) string {
	var b strings.Builder
	b.WriteString(contextBlock(table, habits))
	if control != "" {
		b.WriteString("\nЧто можно менять:\n" + control + "\n" + Commands + "\n")
	}
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

// MoneyParsePrompt — разбор сверки, с которой не справился код. keys — id счёта → алиасы.
func MoneyParsePrompt(text string, keys map[string][]string) string {
	var b strings.Builder
	b.WriteString("Сообщение с остатками по счетам:\n")
	b.WriteString(text)
	b.WriteString("\n\nСчета (ключ: как называются в сообщении):\n")
	ids := make([]string, 0, len(keys))
	for k := range keys {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, k := range ids {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(keys[k], ", "))
	}
	b.WriteString(`Верни только JSON-объект с этими ключами: целое число тенге или null, если суммы в сообщении нет.
«142к» — 142000, «1,12 млн» — 1120000. debt_total — общий долг. Ничего не выдумывай, без пояснений.`)
	return b.String()
}

// MoneyPrompt — последняя строка отчёта о деньгах.
func MoneyPrompt(block string) string {
	return block + `
Напиши одну строку не длиннее 160 символов, начиная с «Узкое место:»: самое важное отклонение от плана и, если есть, одно действие.
Если всё по плану — ответь пустой строкой.`
}

// CleanBottleneck — строка «Узкое место: …»; false — ответ пуст или не в формате.
func CleanBottleneck(raw string) (string, bool) {
	for _, line := range strings.Split(raw, "\n") {
		if line = cleanLine(line); strings.HasPrefix(line, bottleneckPrefix) {
			return Truncate(line, MaxStepRunes), true
		}
	}
	return "", false
}

// MaxAnchorRunes — предел ответа про якорь.
const MaxAnchorRunes = 400

// AnchorPrompt — разбор якоря, который на неделе не сработал (Gollwitzer & Sheeran 2006,
// Wang 2021: план «если — то» с учётом препятствия).
func AnchorPrompt(table, habits, habit string) string {
	return contextBlock(table, habits) + fmt.Sprintf(`
На этой неделе не сработал якорь привычки «%s». Предложи одно, что уместнее по данным:
- заменить якорь на событие, которое и так случается каждый день;
- или запасной план «если мешает X → делаю пол привычки».
Не больше трёх коротких строк, без вступления и без вопросов.`, habit)
}

// CleanAnswer чистит свободный ответ модели и ограничивает длину; false — пусто.
func CleanAnswer(raw string, limit int) (string, bool) {
	lines := make([]string, 0, 4)
	for _, line := range strings.Split(raw, "\n") {
		if line = cleanLine(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "", false
	}
	return Truncate(strings.Join(lines, "\n"), limit), true
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
