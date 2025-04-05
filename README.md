# Sistema de Temperatura por CEP com OpenTelemetry e Zipkin

## Visão Geral

Este projeto consiste em dois microsserviços que trabalham em conjunto para:

- Validar CEPs
- Consultar localidades
- Obter dados meteorológicos
- Rastrear todas as operações com OpenTelemetry e Zipkin


## Pré-requisitos

- Docker 20.10+
- Docker Compose 1.29+
- go 1.23.8

## Configuação

1. Clone o Repositório:
```
git clone https://github.com/math-schenatto/cep-temperature-system.git
cd cep-temperature-system
```

## Executando o Projeto

```
docker-compose up --build
```

Os serviços estarão disponíveis em:

- Serviço A: http://localhost:8080
- Serviço B: http://localhost:8081
- Zipkin UI: http://localhost:9411


## Testando a Aplicação

1. Requisição válida
```
curl -X POST http://localhost:8080/cep \
  -H "Content-Type: application/json" \
  -d '{"cep":"01001000"}'
```

Resposta esperada:
```
{
  "city": "São Paulo",
  "temp_C": 22.5,
  "temp_F": 72.5,
  "temp_K": 295.65
}
```


2. Casos de erro

- CEP inválido (422):
```
curl -X POST http://localhost:8080/cep -d '{"cep":"123"}'
```

- CEP não encontrado (404):
```
curl -X POST http://localhost:8080/cep -d '{"cep":"00000000"}'
```


## Visualizando Traces

Acesse o Zipkin em http://localhost:9411 e:

1. Selecione o serviço (service-a ou service-b)
2. Defina o intervalo de tempo
3. Clique em "Find Traces"