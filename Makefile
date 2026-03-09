.PHONY: build test lint clean download-model

MODEL_BASE  := https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main
MODEL_DIR   := models

build:
	go build -o bin/lumber ./cmd/lumber

test:
	LD_LIBRARY_PATH=$(MODEL_DIR) go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

download-model:
	@mkdir -p $(MODEL_DIR)
	@if [ -f $(MODEL_DIR)/model_quantized.onnx ] && [ -f $(MODEL_DIR)/model_quantized.onnx_data ] && [ -f $(MODEL_DIR)/vocab.txt ]; then \
		echo "Model files already exist in $(MODEL_DIR)/, skipping download."; \
	else \
		echo "Downloading ONNX model (quantized int8, ~23MB)..."; \
		curl -fSL --progress-bar -o $(MODEL_DIR)/model_quantized.onnx       "$(MODEL_BASE)/onnx/model_quantized.onnx"; \
		curl -fSL --progress-bar -o $(MODEL_DIR)/model_quantized.onnx_data  "$(MODEL_BASE)/onnx/model_quantized.onnx_data"; \
		echo "Downloading tokenizer files..."; \
		curl -fSL -o $(MODEL_DIR)/vocab.txt             "$(MODEL_BASE)/vocab.txt"; \
		curl -fSL -o $(MODEL_DIR)/tokenizer_config.json "$(MODEL_BASE)/tokenizer_config.json"; \
		echo "Download complete."; \
	fi
	@if [ ! -f $(MODEL_DIR)/2_Dense/model.safetensors ]; then \
		echo "Downloading projection layer weights (~1.6MB)..."; \
		mkdir -p $(MODEL_DIR)/2_Dense; \
		curl -fSL --progress-bar -o $(MODEL_DIR)/2_Dense/model.safetensors "$(MODEL_BASE)/2_Dense/model.safetensors"; \
		curl -fSL -o $(MODEL_DIR)/2_Dense/config.json                      "$(MODEL_BASE)/2_Dense/config.json"; \
	fi
	@if [ ! -f $(MODEL_DIR)/libonnxruntime.so ]; then \
		ORT_SO=$$(find $$(go env GOMODCACHE) -path "*/yalue/onnxruntime_go@*/test_data/onnxruntime_arm64.so" 2>/dev/null | head -1); \
		if [ -n "$$ORT_SO" ]; then \
			echo "Copying ONNX Runtime shared library from Go module cache..."; \
			cp "$$ORT_SO" $(MODEL_DIR)/libonnxruntime.so; \
		else \
			echo "WARNING: libonnxruntime.so not found. Run 'go mod download' first, then re-run this target."; \
		fi; \
	fi
