package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/zipkin"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

const (
	weatherAPIKey = "ef75abdde5f840bca86181556251603"
	weatherAPIURL = "http://api.weatherapi.com/v1/current.json"
)

type WeatherAPIResponse struct {
	Current struct {
		TempC float64 `json:"temp_c"`
	} `json:"current"`
	Location struct {
		Name string `json:"name"`
	} `json:"location"`
}

type CEPRequest struct {
	CEP string `json:"cep"`
}

type TemperatureResponse struct {
	City  string  `json:"city"`
	TempC float64 `json:"temp_C"`
	TempF float64 `json:"temp_F"`
	TempK float64 `json:"temp_K"`
}

type ViaCEPResponse struct {
	Localidade string `json:"localidade"`
}

func initTracer() (*sdktrace.TracerProvider, error) {
	// Configuração robusta do exporter Zipkin
	exporter, err := zipkin.New(
		"http://zipkin:9411/api/v2/spans",
		zipkin.WithLogger(log.New(os.Stdout, "ZIPKIN", log.LstdFlags)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create zipkin exporter: %w", err)
	}

	// Configuração do resource com metadados do serviço
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("service-b"),
			semconv.ServiceVersion("1.0.0"),
			attribute.String("environment", "production"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Criação do TracerProvider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Configuração do propagador para tracing distribuído
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	otel.SetTracerProvider(tp)
	return tp, nil
}

func fetchCityFromCEP(ctx context.Context, cep string) (string, error) {
	tracer := otel.Tracer("service-b")
	ctx, span := tracer.Start(ctx, "fetch-city-from-cep")
	defer span.End()

	span.SetAttributes(
		attribute.String("cep", cep),
		attribute.String("api.url", "viacep.com.br"),
	)

	url := fmt.Sprintf("https://viacep.com.br/ws/%s/json/", cep)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to create request")
		return "", err
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "API request failed")
		return "", err
	}
	defer resp.Body.Close()

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode == http.StatusBadRequest {
		span.SetStatus(codes.Error, "invalid zipcode")
		return "", fmt.Errorf("invalid zipcode")
	}

	if resp.StatusCode == http.StatusNotFound {
		span.SetStatus(codes.Error, "can not find zipcode")
		return "", fmt.Errorf("can not find zipcode")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to read response")
		return "", err
	}

	var viaCEPResp ViaCEPResponse
	if err := json.Unmarshal(body, &viaCEPResp); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to decode response")
		return "", err
	}

	if viaCEPResp.Localidade == "" {
		span.SetStatus(codes.Error, "city not found")
		return "", fmt.Errorf("city not found")
	}

	span.SetAttributes(attribute.String("city", viaCEPResp.Localidade))
	return viaCEPResp.Localidade, nil
}

func fetchTemperature(ctx context.Context, city string) (float64, error) {
	tracer := otel.Tracer("service-b")
	ctx, span := tracer.Start(ctx, "fetch-temperature")
	defer span.End()

	span.SetAttributes(
		attribute.String("city", city),
		attribute.String("weather.api", "weatherapi.com"),
	)

	encodedCity := url.QueryEscape(city)
	url := fmt.Sprintf("%s?key=%s&q=%s&aqi=no", weatherAPIURL, weatherAPIKey, encodedCity)
	span.SetAttributes(attribute.String("api.url", url))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to create request")
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "API request failed")
		return 0, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		span.SetStatus(codes.Error, "API returned error")
		return 0, fmt.Errorf("API error: %s", string(body))
	}

	var weatherResp WeatherAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&weatherResp); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to decode response")
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	if weatherResp.Current.TempC == 0 {
		span.SetStatus(codes.Error, "Invalid temperature data")
		return 0, fmt.Errorf("invalid temperature data")
	}

	span.SetAttributes(
		attribute.Float64("temperature.c", weatherResp.Current.TempC),
		attribute.String("location", weatherResp.Location.Name),
	)

	return weatherResp.Current.TempC, nil
}
func handleTemperature(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("service-b")
	ctx, span := tracer.Start(r.Context(), "handleTemperature")
	defer span.End()

	span.SetAttributes(
		attribute.String("http.method", r.Method),
		attribute.String("http.path", r.URL.Path),
	)

	var req CEPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Invalid request body")
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	span.SetAttributes(attribute.String("cep", req.CEP))

	city, err := fetchCityFromCEP(ctx, req.CEP)
	if err != nil {
		span.RecordError(err)
		switch err.Error() {
		case "invalid zipcode":
			span.SetStatus(codes.Error, "Invalid zipcode")
			http.Error(w, "invalid zipcode", http.StatusUnprocessableEntity)
		case "city not found":
			span.SetStatus(codes.Error, "Zipcode not found")
			http.Error(w, "can not find zipcode", http.StatusNotFound)
		default:
			span.SetStatus(codes.Error, "Failed to fetch city")
			http.Error(w, "failed to fetch city", http.StatusInternalServerError)
		}
		return
	}

	tempC, err := fetchTemperature(ctx, city)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to fetch temperature")
		http.Error(w, "failed to fetch temperature", http.StatusInternalServerError)
		return
	}

	tempF := tempC*1.8 + 32
	tempK := tempC + 273

	response := TemperatureResponse{
		City:  city,
		TempC: tempC,
		TempF: tempF,
		TempK: tempK,
	}

	span.SetAttributes(
		attribute.Float64("temperature.c", tempC),
		attribute.Float64("temperature.f", tempF),
		attribute.Float64("temperature.k", tempK),
	)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		span.RecordError(err)
	}
}

func main() {
	tp, err := initTracer()
	if err != nil {
		log.Fatalf("Failed to initialize tracer: %v", err)
	}
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("Failed to shutdown tracer: %v", err)
		}
	}()

	// Configuração do servidor HTTP
	http.HandleFunc("/temperature", handleTemperature)
	log.Println("Service B listening on :8081")
	if err := http.ListenAndServe(":8081", nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
