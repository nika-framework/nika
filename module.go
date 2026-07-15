package nika

type Module interface {
	Imports() []Module
	Controllers() []interface{}
	Providers() []interface{}
	Exports() []interface{}
}
