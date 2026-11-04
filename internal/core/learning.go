package core

import (
	"context"

	"floor/internal/learning"
	"floor/internal/render"
)

const answerLearning = "learning"

// learningWeek — тема недели, в которую входит дата. false — программа не идёт.
func (a *App) learningWeek(ctx context.Context, date string) (learning.Week, bool) {
	topics, err := learning.Load(a.conf().LearningFile)
	if err != nil {
		a.Log.Warn("learning_invalid", "error", err)
	}
	raw, err := a.Store.AnswersByPeriod(ctx, answerLearning)
	if err != nil {
		a.Log.Warn("learning_failed", "error", err)
		return learning.Week{}, false
	}
	answers := learning.Answers{}
	for period, v := range raw {
		answers[period] = v == 1
	}
	return learning.Current(topics, a.conf().LearningStart, date, answers)
}

// learningBlock — блок понедельника; пусто в другие дни и вне программы.
func (a *App) learningBlock(ctx context.Context, date string) string {
	w, ok := a.learningWeek(ctx, date)
	if !ok {
		return ""
	}
	return render.LearningBlock(w.Index, w.Total, w.Topic.Title, w.Topic.Action, w.Repeat)
}
