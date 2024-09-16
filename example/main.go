package main

import (
	"github.com/Jeffail/benthos/v3/lib/service"
	_ "github.com/le-vlad/benthos-mongodb-plugin/lib"
)

func main() {
	service.Run()
}
