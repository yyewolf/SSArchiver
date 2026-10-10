package views

import "github.com/a-h/templ"

// sseSwap spreads sse-swap onto an element when it should listen for live
// updates (nil attributes spread nothing).
func sseSwap(live bool, event string) templ.Attributes {
	if !live {
		return nil
	}
	return templ.Attributes{"sse-swap": event}
}
