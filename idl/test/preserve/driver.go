package main

import (
	"fmt"
	"os"
	rec "preserve.test/generated/go/rec"
)

func main() {
	defer func() {
		if e := recover(); e != nil {
			if r, ok := e.(*rec.Refusal); ok {
				fmt.Print(r.Word)
				return
			}
			panic(e)
		}
	}()
	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	r, err := rec.Decode(input)
	if err != nil {
		fmt.Print(err.(*rec.Refusal).Word)
		return
	}
	scope := os.Args[3]
	if scope == "mutate" {
		r.Id = "edited"
		if r.Child != nil {
			r.Child.Name = "edited-child"
		}
		for i := range r.Children {
			r.Children[i].Name = "edited-child"
		}
	}
	if scope != "mutate" && scope != "read" {
		raw, err := os.ReadFile(os.Args[5])
		if err != nil {
			panic(err)
		}
		target := &r.Extras
		if scope == "child" {
			target = &r.Child.Extras
		}
		if scope == "repeated" {
			target = &r.Children[0].Extras
		}
		if *target == nil {
			*target = map[string]rec.Raw{}
		}
		key := os.Args[4]
		if scope == "bad-key" {
			key = string([]byte{255})
		}
		(*target)[key] = rec.Raw(raw)
	}
	output := rec.Encode(r)
	if _, err := rec.Decode(output); err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[2], output, 0600); err != nil {
		panic(err)
	}
	fmt.Print("ok")
}
