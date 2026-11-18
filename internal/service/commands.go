// Package service contains application behavior independent of Telegram.
package service

type Commands struct{}

func (Commands) Start() string {
	return "Привет! Я помощник по учебным задачам и занятиям.\n\n" +
		"Список доступных действий: /help"
}

func (Commands) Help() string {
	return "Доступные команды:\n/start — знакомство с помощником\n/help — эта справка"
}
