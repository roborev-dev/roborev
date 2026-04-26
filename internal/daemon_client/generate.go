package daemonclient

//go:generate go run ./openapi_generate -openapi-3.0 -o openapi-3.0.json
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.6.0 --config oapi-codegen.yaml openapi-3.0.json
