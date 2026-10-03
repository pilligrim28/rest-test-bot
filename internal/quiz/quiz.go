// Package quiz содержит вопросы теста, матрицу подсчёта и тексты результатов.
package quiz

// Category — категория результата. Совпадает с буквами из ТЗ.
type Category string

const (
	// WorkInHead — «работа продолжается в мыслях» (ответы А).
	WorkInHead Category = "A"
	// AutoFill — «пауза автоматически заполняется занятиями» (ответы Б).
	AutoFill Category = "B"
	// PostponedRest — «отдых откладывается до сильной усталости» (ответы В).
	PostponedRest Category = "C"
)

// AllCategories — порядок категорий для детерминированной сортировки.
var AllCategories = []Category{WorkInHead, AutoFill, PostponedRest}

// CategoryNames — человекочитаемые названия для отчётов и выгрузок.
var CategoryNames = map[Category]string{
	WorkInHead:    "Работа продолжается в мыслях",
	AutoFill:      "Автоматическое заполнение пауз",
	PostponedRest: "Откладывание отдыха до усталости",
}

// QuestionCount — сколько вопросов в тесте.
const QuestionCount = 5

// MinAnswersToEvaluate — минимум ответов, ниже которого результат не выдаём.
const MinAnswersToEvaluate = 3

// Option — вариант ответа.
type Option struct {
	Text     string
	Category Category
}

// Question — один вопрос теста с тремя вариантами.
type Question struct {
	Number  int
	Prompt  string
	Options []Option
}

// Answers хранит выбор пользователя: номер вопроса (1-based) → индекс варианта (0..2).
// Отсутствие ключа означает, что вопрос пропущен.
type Answers map[int]int

// AnswerCount возвращает число отвеченных вопросов.
func (a Answers) AnswerCount() int {
	n := 0
	for q, opt := range a {
		if q >= 1 && q <= QuestionCount && opt >= 0 && opt < 3 {
			n++
		}
	}
	return n
}

// Outcome — результат подсчёта баллов.
type Outcome struct {
	// Categories — одна категория-победитель или две при равенстве баллов.
	Categories []Category
	// Scores — баллы по каждой категории.
	Scores map[Category]int
	// Answered — сколько вопросов реально отвечено.
	Answered int
	// Insufficient — true, если ответов меньше MinAnswersToEvaluate.
	Insufficient bool
}

// IsTie сообщает, что результат разделился между двумя привычками.
func (o Outcome) IsTie() bool { return len(o.Categories) > 1 }

// Evaluate считает баллы по матрице ответов.
//
// Правила из ТЗ:
//   - каждый ответ даёт 1 балл своей категории;
//   - показываем категорию с максимальным баллом;
//   - при равенстве максимумов показываем обе, без случайного выбора;
//   - при числе ответов меньше трёх результат не выдаём.
func Evaluate(answers Answers) Outcome {
	scores := map[Category]int{
		WorkInHead:    0,
		AutoFill:      0,
		PostponedRest: 0,
	}

	for q, opt := range answers {
		if q < 1 || q > QuestionCount || opt < 0 || opt > 2 {
			continue
		}
		question := questionAt(q)
		if question == nil {
			continue
		}
		scores[question.Options[opt].Category]++
	}

	outcome := Outcome{Scores: scores, Answered: answers.AnswerCount()}
	if outcome.Answered < MinAnswersToEvaluate {
		outcome.Insufficient = true
		return outcome
	}

	best := 0
	for _, c := range AllCategories {
		if scores[c] > best {
			best = scores[c]
		}
	}

	for _, c := range AllCategories {
		if scores[c] == best && best > 0 {
			outcome.Categories = append(outcome.Categories, c)
		}
	}

	return outcome
}

func questionAt(number int) *Question {
	for i := range Questions {
		if Questions[i].Number == number {
			return &Questions[i]
		}
	}
	return nil
}

// ByNumber возвращает вопрос по его номеру (1-based).
func ByNumber(number int) (Question, bool) {
	if q := questionAt(number); q != nil {
		return *q, true
	}
	return Question{}, false
}

// Label возвращает подпись кнопки для варианта ответа.
func Label(index int) string { return []string{"1", "2", "3"}[index] }
