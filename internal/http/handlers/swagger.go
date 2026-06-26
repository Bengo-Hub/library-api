package handlers

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"gopkg.in/yaml.v3"
)

//go:embed swagger.yaml
var swaggerSpecYAML []byte

var swaggerSpec []byte

func init() {
	var spec map[string]interface{}
	if err := yaml.Unmarshal(swaggerSpecYAML, &spec); err == nil {
		if jsonData, err := json.Marshal(spec); err == nil {
			swaggerSpec = jsonData
		}
	}
}

// OpenAPIJSON serves the OpenAPI/Swagger JSON specification.
func OpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(swaggerSpec)
}

// SwaggerUI serves the Swagger UI HTML page (SwaggerUIBundle from CDN).
func SwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
  <head>
    <meta charset="UTF-8">
    <title>Library Service API Docs</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
    <script>
      window.onload = () => {
        const specUrl = window.location.protocol + '//' + window.location.host + '/api/v1/openapi.json';
        window.ui = SwaggerUIBundle({
          url: specUrl,
          dom_id: '#swagger-ui',
          presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
          layout: "BaseLayout",
          deepLinking: true,
          filter: true,
          persistAuthorization: true,
          requestInterceptor: (request) => {
            if (request.headers && request.headers.Authorization) {
              const authHeader = request.headers.Authorization;
              if (!/^bearer\s+/i.test(authHeader)) {
                request.headers.Authorization = 'Bearer ' + authHeader.trim();
              }
            }
            return request;
          }
        })
      }
    </script>
  </body>
</html>`))
}
