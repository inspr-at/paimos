// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import "testing"

func TestCommandPaletteShortcutGrammarAndReservedCollisions(t *testing.T) {
	for _, value := range []string{"Mod+" + "KeyK", "Ctrl+Shift+" + "KeyK", "Meta+Alt+" + "Slash", "Alt+" + "Digit0", "Ctrl+Meta+Shift+" + "Space"} {
		if valid, collision := classifyCommandPaletteShortcut(value); !valid || collision {
			t.Fatalf("safe chord %q classified valid=%v collision=%v", value, valid, collision)
		}
	}
	for _, value := range []string{"KeyK", "Shift+KeyK", "Mod+Ctrl+KeyK", "Shift+Ctrl+KeyK", "Ctrl+Escape", "Ctrl+k", "Ctrl++KeyK", " Ctrl+KeyK"} {
		if valid, collision := classifyCommandPaletteShortcut(value); valid || collision {
			t.Fatalf("invalid chord %q classified valid=%v collision=%v", value, valid, collision)
		}
	}
	for _, value := range []string{"Mod+" + "KeyR", "Ctrl+Shift+" + "KeyL", "Meta+Alt+" + "KeyW", "Ctrl+" + "Digit1", "Meta+Shift+" + "Digit9"} {
		if valid, collision := classifyCommandPaletteShortcut(value); valid || !collision {
			t.Fatalf("reserved chord %q classified valid=%v collision=%v", value, valid, collision)
		}
	}
}
