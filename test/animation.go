package main

type IAnimation interface {
	Eat()
	Run()
	Walk()
	GetAge() int
}

type Dog struct {
	age int
}

func (obj *Dog) Eat() {

}

type Cat struct {
	color int
	age   int
}
