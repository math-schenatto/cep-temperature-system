package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/zipkin"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

type CEPRequest struct {
	CEP string `json:"cep"`
}

func initTracer() (*sdktrace.TracerProvider, error) {
	// Configura o exporter Zipkin
	exporter, err := zipkin.New(
		"http://zipkin:9411/api/v2/spans",
		zipkin.WithLogger(log.New(os.Stdout, "zipkin", log.LstdFlags)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create zipkin exporter: %w", err)
	}

	// Configura o resource com informações do serviço
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceName("service-a"),
			semconv.ServiceVersion("1.0.0"),
			attribute.String("environment", "development"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Cria o TracerProvider
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Configura o propagador para tracing distribuído
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	otel.SetTracerProvider(tp)
	return tp, nil
}

func isValidCEP(cep string) bool {
	if len(cep) != 8 {
		return false
	}
	_, err := strconv.Atoi(cep)
	return err == nil
}

func handleCEP(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("service-a")
	ctx, span := tracer.Start(r.Context(), "handleCEP")
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

	// Validação do CEP
	ctx, validateSpan := tracer.Start(ctx, "validate-cep")
	if !isValidCEP(req.CEP) {
		validateSpan.RecordError(fmt.Errorf("invalid zipcode"))
		validateSpan.SetStatus(codes.Error, "Invalid zipcode")
		validateSpan.End()
		http.Error(w, "invalid zipcode", http.StatusUnprocessableEntity)
		return
	}
	validateSpan.End()

	// Chamada ao Service B
	serviceBURL := "http://service-b:8081/temperature"
	reqBody, err := json.Marshal(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "Failed to marshal request")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	ctx, callSpan := tracer.Start(ctx, "call-service-b")
	defer callSpan.End()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", serviceBURL, bytes.NewBuffer(reqBody))
	if err != nil {
		callSpan.RecordError(err)
		callSpan.SetStatus(codes.Error, "Failed to create request")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Propagação do contexto para tracing distribuído
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		callSpan.RecordError(err)
		callSpan.SetStatus(codes.Error, "Failed to call service")
		http.Error(w, "failed to call service b", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	callSpan.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		callSpan.RecordError(err)
		callSpan.SetStatus(codes.Error, "Failed to read response")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(body); err != nil {
		span.RecordError(err)
	}
}

func main() {
	// Inicializa o tracer
	tp, err := initTracer()
	if err != nil {
		log.Fatalf("Failed to initialize tracer: %v", err)
	}
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("Failed to shutdown tracer: %v", err)
		}
	}()

	// Configura o servidor HTTP
	http.HandleFunc("/cep", handleCEP)
	log.Println("Service A listening on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
