package bot

import (
	"strings"

	"github.com/pilligrim28/rest-test-bot/internal/telegramapi"
)

// UserCommands — команды, которые видят все пользователи в меню Telegram.
var UserCommands = []telegramapi.Command{
	{Name: "start", Description: "Начать: приветствие и тест"},
	{Name: "test", Description: "Пройти тест заново"},
	{Name: "mydata", Description: "Что бот о вас хранит"},
	{Name: "forget", Description: "Отозвать согласие и удалить данные"},
	{Name: "privacy", Description: "Как бот обращается с данными"},
	{Name: "help", Description: "Справка"},
}

// AdminCommands — команды, которые видят только администраторы (ADMIN_IDS).
var AdminCommands = []telegramapi.Command{
	{Name: "stats", Description: "Статистика"},
	{Name: "users", Description: "Список подписчиков"},
	{Name: "broadcast", Description: "Рассылка подписчикам"},
	{Name: "send", Description: "Личное сообщение: /send ID текст"},
	{Name: "cancel", Description: "Отменить рассылку"},
	{Name: "admin", Description: "Справка для админа"},
}

// commandList форматирует команды для справки.
func commandList(cmds []telegramapi.Command) string {
	var sb strings.Builder
	for _, c := range cmds {
		sb.WriteString("/" + c.Name + " — " + c.Description + "\n")
	}
	return sb.String()
}

func helpText(isAdmin bool) string {
	text := "Я провожу короткий тест про вечерний отдых.\n\n" + commandList(UserCommands) +
		"\nОтвечать на вопросы можно кнопками или цифрами 1–3."
	if isAdmin {
		text += "\n\n" + adminHelpText()
	}
	return text
}

func adminHelpText() string {
	return "Команды администратора:\n" + commandList(AdminCommands)
}
