package aisvc

import "testing"

func TestPromptStoreUpsertAndReset(t *testing.T) {
	s := NewPromptStore()
	got, ok := s.Get("proxy_extract")
	if !ok || !got.Default || !got.Builtin {
		t.Fatalf("builtin prompt missing or flags wrong: %+v ok=%v", got, ok)
	}

	edited := got
	edited.System = "你是自定义提取器"
	edited.User = "内容：{{.Content}}"
	s.Upsert(edited)

	got2, _ := s.Get("proxy_extract")
	if got2.Default {
		t.Fatal("edited builtin should be marked non-default")
	}
	if got2.System != "你是自定义提取器" {
		t.Fatalf("system not persisted: %q", got2.System)
	}

	// delete on builtin resets to default
	if !s.Delete("proxy_extract") {
		t.Fatal("delete builtin should succeed (reset)")
	}
	got3, _ := s.Get("proxy_extract")
	if got3.System != DefaultSystem("proxy_extract") || !got3.Default {
		t.Fatalf("reset failed: %+v", got3)
	}

	// custom prompt delete removes entirely
	s.Upsert(Prompt{Name: "custom", Title: "自定义", System: "x"})
	if !s.Delete("custom") {
		t.Fatal("delete custom should succeed")
	}
	if _, ok := s.Get("custom"); ok {
		t.Fatal("custom prompt should be gone")
	}
}

func TestPromptStoreListClones(t *testing.T) {
	s := NewPromptStore()
	list := s.List()
	list[0].Title = "mutated"
	again, _ := s.Get(list[0].Name)
	if again.Title == "mutated" {
		t.Fatal("List must return a defensive copy")
	}
}
