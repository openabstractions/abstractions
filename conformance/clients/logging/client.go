package main

import (
	"fmt"
	"os"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	logging "github.com/openabstractions/abstraction-logging/go/client"
)

func main() {
	c := facade.Discover().Log()
	switch os.Args[1] {
	case "write":
		if err := c.Log(0, "from go\nsecond line ☃", map[string]string{"language": "go", "empty": ""}); err != nil {
			panic(err)
		}
	case "bad-schema":
		err := c.Write(logging.Record{Schema: 2, Time: "2026-09-11T12:00:00.000000Z", Msg: "must not be written"})
		if err == nil {
			panic("invalid schema accepted")
		}
	case "absent":
		if err := c.Log(0, "must not create local state", nil); err == nil {
			panic("absent service reported success")
		}
	default:
		panic("unknown case")
	}
	fmt.Println("PASS: Go", os.Args[1])
}
