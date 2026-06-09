package main

import (
	"context"
	"sync"

	"fyne.io/systray"
)

type desktopTray struct {
	ctx      context.Context
	cancel   context.CancelFunc
	end      func()
	openItem *systray.MenuItem
	quitItem *systray.MenuItem
	once     sync.Once
}

func (a *App) startTray() {
	if !traySupported() {
		return
	}
	a.mu.Lock()
	if a.tray != nil {
		a.mu.Unlock()
		return
	}
	t := &desktopTray{}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	a.tray = t
	a.mu.Unlock()

	t.end = startDesktopTray(func() {
		systray.SetIcon(trayIconBytes)
		systray.SetTitle("Reasonix")
		systray.SetTooltip("Reasonix")
		systray.SetOnTapped(func() { a.showFromTray() })
		// Keep secondary/right-click on systray's native menu path.
		systray.SetOnSecondaryTapped(nil)

		labels := trayMenuLabels(a.trayLocale())
		t.openItem = systray.AddMenuItem(labels.openTitle, labels.openTooltip)
		t.quitItem = systray.AddMenuItem(labels.quitTitle, labels.quitTooltip)

		a.mu.Lock()
		a.trayReady = true
		a.mu.Unlock()

		go func() {
			for {
				select {
				case <-t.ctx.Done():
					return
				case _, ok := <-t.openItem.ClickedCh:
					if !ok {
						return
					}
					a.showFromTray()
				}
			}
		}()
		go func() {
			for {
				select {
				case <-t.ctx.Done():
					return
				case _, ok := <-t.quitItem.ClickedCh:
					if !ok {
						return
					}
					a.quitFromTray()
				}
			}
		}()
	}, func() {
		a.mu.Lock()
		a.trayReady = false
		a.mu.Unlock()
	})
}

func (a *App) stopTray() {
	a.mu.RLock()
	t := a.tray
	a.mu.RUnlock()
	if t == nil || t.end == nil {
		return
	}
	t.cancel()
	t.once.Do(t.end)
}

func (a *App) updateTrayLocale(locale string) {
	a.mu.RLock()
	t := a.tray
	a.mu.RUnlock()
	if t == nil || t.openItem == nil || t.quitItem == nil {
		return
	}
	labels := trayMenuLabels(locale)
	t.openItem.SetTitle(labels.openTitle)
	t.openItem.SetTooltip(labels.openTooltip)
	t.quitItem.SetTitle(labels.quitTitle)
	t.quitItem.SetTooltip(labels.quitTooltip)
}

func (a *App) trayLocale() string {
	cfg, _, err := a.loadDesktopUserConfigForEdit()
	if err != nil {
		return ""
	}
	return cfg.DesktopLanguage()
}

func (a *App) showFromTray() {
	ctx := a.ctx
	if ctx == nil {
		return
	}
	showFromBackground(ctx)
}

func (a *App) quitFromTray() {
	a.quitApp()
}

type trayLabels struct {
	openTitle   string
	openTooltip string
	quitTitle   string
	quitTooltip string
}

func trayMenuLabels(locale string) trayLabels {
	if locale == "zh" {
		return trayLabels{
			openTitle:   "打开",
			openTooltip: "打开 Reasonix 窗口",
			quitTitle:   "退出",
			quitTooltip: "退出 Reasonix",
		}
	}
	return trayLabels{
		openTitle:   "Open",
		openTooltip: "Open the Reasonix window",
		quitTitle:   "Quit",
		quitTooltip: "Quit Reasonix",
	}
}
