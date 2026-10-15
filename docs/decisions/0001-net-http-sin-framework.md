---
title: "ADR 0001: net/http sin framework"
status: accepted
date: 2026-10-14
deciders: oscar
---

# ADR 0001: net/http sin framework

## Contexto

El servicio expone tres endpoints de negocio (`POST /payments`, `GET /payments/{id}` y `GET /payments?merchant_id=&status=`) y un health check. El enunciado permite usar la librería estándar o cualquier framework.

## Decisión

Usar `net/http` de la librería estándar, sin framework:

- El router de Go 1.22+ ya resuelve el método y los parámetros de ruta (`GET /payments/{id}`).
- Middlewares propios: request id, access log y recuperación de panics.
- `log/slog` para logs estructurados en JSON.
- `pgx` como única dependencia, porque la librería estándar no trae driver de Postgres.

## Consecuencias

**Positivas**
- Cada línea del flujo HTTP se puede explicar: no hay comportamiento oculto en un framework.
- Los handlers son `http.Handler` estándar: se prueban con `httptest` sin abrir puertos, y pasar a chi no requiere reescribirlos.
- El middleware de request id es justamente el requisito de correlación, y queda en código propio.

**Negativas**
- Los middlewares se escriben a mano (menos de 100 líneas).
- No hay binding ni validación automática. La validación vive en el dominio, que es donde corresponden las reglas de negocio.

## Alternativas consideradas

- **Fiber**: se basa en fasthttp y no en `net/http`, y reutiliza el contexto entre requests (sus valores no son válidos fuera del handler). Su ventaja de rendimiento no aplica aquí: el tiempo se va en Postgres y en el adquirente, no en el router.
- **Gin / Echo**: aportan routing, binding y middlewares que con tres endpoints no se necesitan.
- **chi**: es compatible con `net/http`. Sería el paso natural si crece el número de endpoints.
