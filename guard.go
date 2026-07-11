package nika

func (a *App) AddGuard(name string, guardFn GuardFunc) {
	a.guards[name] = guardFn
}
