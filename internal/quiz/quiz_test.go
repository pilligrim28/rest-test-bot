package quiz

import "testing"

func TestEvaluateSingleWinner(t *testing.T) {
	// Три ответа категории A, по одному B и C.
	answers := Answers{1: 0, 2: 0, 3: 0, 4: 1, 5: 2}

	got := Evaluate(answers)
	if got.Insufficient {
		t.Fatal("Insufficient = true, want false")
	}
	if len(got.Categories) != 1 || got.Categories[0] != WorkInHead {
		t.Fatalf("Categories = %v, want [A]", got.Categories)
	}
	if got.Scores[WorkInHead] != 3 {
		t.Errorf("scores[A] = %d, want 3", got.Scores[WorkInHead])
	}
}

func TestEvaluateTie(t *testing.T) {
	// По два балла A и B, один C — максимум делят две категории.
	answers := Answers{1: 0, 2: 0, 3: 1, 4: 1, 5: 2}

	got := Evaluate(answers)
	if !got.IsTie() {
		t.Fatalf("IsTie = false, categories = %v", got.Categories)
	}
	if len(got.Categories) != 2 {
		t.Fatalf("len(Categories) = %d, want 2", len(got.Categories))
	}
	// Порядок детерминирован: A всегда раньше B.
	if got.Categories[0] != WorkInHead || got.Categories[1] != AutoFill {
		t.Errorf("Categories = %v, want [A B]", got.Categories)
	}
}

func TestEvaluateThreeWayTie(t *testing.T) {
	// По одному ответу каждой категории, два вопроса пропущены.
	// Максимум делят все три категории — результат не выбирается случайно.
	answers := Answers{1: 0, 2: 1, 3: 2}

	got := Evaluate(answers)
	if got.Insufficient {
		t.Fatal("Insufficient = true, want false")
	}
	if len(got.Categories) != 3 {
		t.Fatalf("Categories = %v, want three winners", got.Categories)
	}
}

func TestEvaluateInsufficientAnswers(t *testing.T) {
	answers := Answers{1: 0, 2: 1} // только два ответа

	got := Evaluate(answers)
	if !got.Insufficient {
		t.Fatal("Insufficient = false, want true")
	}
	if len(got.Categories) != 0 {
		t.Errorf("Categories = %v, want none", got.Categories)
	}
}

func TestEvaluateIgnoresInvalidInput(t *testing.T) {
	answers := Answers{1: 0, 2: 0, 3: 0, 6: 0, 4: 9, 5: -1}

	got := Evaluate(answers)
	if got.Answered != 3 {
		t.Errorf("Answered = %d, want 3", got.Answered)
	}
	if len(got.Categories) != 1 || got.Categories[0] != WorkInHead {
		t.Errorf("Categories = %v, want [A]", got.Categories)
	}
}

func TestAnswerCount(t *testing.T) {
	answers := Answers{1: 0, 2: 1, 3: 2}
	if got := answers.AnswerCount(); got != 3 {
		t.Errorf("AnswerCount = %d, want 3", got)
	}
}

func TestEveryCategoryReachable(t *testing.T) {
	// Каждая категория должна победить, если все ответы одного типа.
	for idx, want := range []Category{WorkInHead, AutoFill, PostponedRest} {
		answers := Answers{}
		for q := 1; q <= QuestionCount; q++ {
			answers[q] = idx
		}
		got := Evaluate(answers)
		if len(got.Categories) != 1 || got.Categories[0] != want {
			t.Errorf("all-%d answers → %v, want [%s]", idx, got.Categories, want)
		}
	}
}

func TestQuestionsWellFormed(t *testing.T) {
	if len(Questions) != QuestionCount {
		t.Fatalf("len(Questions) = %d, want %d", len(Questions), QuestionCount)
	}
	for i, q := range Questions {
		if q.Number != i+1 {
			t.Errorf("Questions[%d].Number = %d, want %d", i, q.Number, i+1)
		}
		if len(q.Options) != 3 {
			t.Errorf("Questions[%d] has %d options, want 3", i, len(q.Options))
		}
		for j, opt := range q.Options {
			if AllCategories[j] != opt.Category {
				t.Errorf("Questions[%d].Options[%d].Category = %s, want %s",
					i, j, opt.Category, AllCategories[j])
			}
		}
	}
}
