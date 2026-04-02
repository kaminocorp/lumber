.PHONY: build test lint clean download-model download-ort

VERSION     ?= dev
MODEL_BASE  := https://huggingface.co/MongoDB/mdbr-leaf-mt/resolve/main
MODEL_DIR   := models
ORT_VERSION := 1.24.1

build:
	go build -ldflags "-s -w -X github.com/kaminocorp/lumber/internal/config.Version=$(VERSION)" \
		-o bin/lumber ./cmd/lumber

test:
	LD_LIBRARY_PATH=$(MODEL_DIR) DYLD_LIBRARY_PATH=$(MODEL_DIR) go test ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/

download-ort:
	@if [ ! -f $(MODEL_DIR)/libonnxruntime.so ] && [ ! -f $(MODEL_DIR)/libonnxruntime.dylib ]; then \
		OS=$$(go env GOOS); ARCH=$$(go env GOARCH); \
		case "$$OS-$$ARCH" in \
			linux-amd64)  ORT_ARCH="linux-x64";    ORT_LIB="libonnxruntime.so.$(ORT_VERSION)";    DEST="libonnxruntime.so" ;; \
			linux-arm64)  ORT_ARCH="linux-aarch64"; ORT_LIB="libonnxruntime.so.$(ORT_VERSION)";    DEST="libonnxruntime.so" ;; \
			darwin-arm64) ORT_ARCH="osx-arm64";     ORT_LIB="libonnxruntime.$(ORT_VERSION).dylib"; DEST="libonnxruntime.dylib" ;; \
			*) echo "Unsupported platform: $$OS/$$ARCH"; exit 1 ;; \
		esac; \
		echo "Downloading ONNX Runtime $$ORT_ARCH $(ORT_VERSION)..."; \
		curl -fSL "https://github.com/microsoft/onnxruntime/releases/download/v$(ORT_VERSION)/onnxruntime-$$ORT_ARCH-$(ORT_VERSION).tgz" \
			| tar xz -C /tmp; \
		cp "/tmp/onnxruntime-$$ORT_ARCH-$(ORT_VERSION)/lib/$$ORT_LIB" "$(MODEL_DIR)/$$DEST"; \
		rm -rf "/tmp/onnxruntime-$$ORT_ARCH-$(ORT_VERSION)"; \
		echo "ONNX Runtime library installed: $(MODEL_DIR)/$$DEST"; \
	else \
		echo "ONNX Runtime library already exists, skipping download."; \
	fi

download-model: download-ort
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
