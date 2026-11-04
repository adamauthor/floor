package render

import (
	"fmt"
	"html"

	"floor/internal/habits"
)

// HabitEmoji — значок привычки или точка.
func HabitEmoji(h habits.Habit) string {
	if h.Emoji != "" {
		return h.Emoji
	}
	return "•"
}

// WorkoutDone — тренировка записана: строка вечера без кнопки.
func WorkoutDone(h habits.Habit, minutes int) string {
	return fmt.Sprintf("%s %s засчитана: %d мин", HabitEmoji(h), h.Title, minutes)
}

// WorkoutMorning — строка утра про недельную привычку.
func WorkoutMorning(h habits.Habit, done int, monday bool) string {
	if monday {
		return fmt.Sprintf("%s Новая неделя: %d из %d", HabitEmoji(h), done, h.PerWeek)
	}
	return fmt.Sprintf("%s Тренировки на неделе: %d из %d", HabitEmoji(h), done, h.PerWeek)
}

// weekResult — итог недели недельной привычки: 3/3, 2/3 (пол), 1/3.
func weekResult(h habits.Habit, done int) string {
	s := fmt.Sprintf("%d/%d", done, h.PerWeek)
	if done < h.PerWeek && done >= h.WeekFloor() {
		s += " (пол)"
	}
	return s
}

// LearningBlock — тема недели в утреннем сообщении понедельника.
func LearningBlock(index, total int, title, action string, repeat bool) string {
	head := fmt.Sprintf("📚 Неделя %d из %d · %s", index+1, total, html.EscapeString(title))
	if repeat {
		head += " (повтор)"
	}
	return head + "\nДействие: " + html.EscapeString(action)
}

// LearningQuestion — вопрос недели про действие.
func LearningQuestion(title, action string) string {
	return "📚 " + html.EscapeString(title) + "\nДействие: " + html.EscapeString(action)
}

// QueueOffer — предложение запустить привычку из очереди.
func QueueOffer(h habits.Habit, days, of int) string {
	return fmt.Sprintf("Пол держится %d из %d дней. Запускаем «%s» с понедельника?", days, of, html.EscapeString(HabitName(h)))
}

// SocialLine — строка воскресной сводки про людей.
func SocialLine(met bool) string {
	if met {
		return "👥 Люди: были"
	}
	return "👥 Люди: не было"
}
