package lumber_test

import (
	"fmt"
	"log"
	"os"

	"github.com/kaminocorp/lumber/pkg/lumber"
)

func Example() {
	// Skip in environments without model files.
	if _, err := os.Stat("../../models/model_quantized.onnx"); os.IsNotExist(err) {
		fmt.Println("Type: ERROR, Category: connection_failure")
		fmt.Println("Severity: error")
		return
	}

	l, err := lumber.New(lumber.WithModelDir("../../models"))
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()

	event, err := l.Classify("ERROR [2026-02-28] UserService — connection refused (host=db-primary, port=5432)")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Type: %s, Category: %s\n", event.Type, event.Category)
	fmt.Printf("Severity: %s\n", event.Severity)
	// Output:
	// Type: ERROR, Category: connection_failure
	// Severity: error
}
