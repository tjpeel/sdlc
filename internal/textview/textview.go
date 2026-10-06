// Package textview provides plain terminal headings and explicit status labels.
package textview

func Heading(title string) string { return "== " + title + " ==" }

func Status(category, text string) string { return "[" + category + "] " + text }
