---
title: "ADR 0004: Montos como enteros en la unidad mínima"
status: accepted
date: 2026-10-14
deciders: oscar
---

# ADR 0004: Montos como enteros en la unidad mínima

## Contexto

El enunciado pide recibir un monto, pero no define cómo representarlo. Un servicio de pagos no puede tener errores de redondeo, y `float64` no representa exactamente muchos valores decimales: sumar 0.1 y 0.2 como `float64` da 0.30000000000000004.

## Decisión

- Los montos son `int64` en la unidad mínima de la moneda: pesos para CLP, que no tiene decimales, y centavos para USD. Es la misma convención que usan procesadores como Stripe y Adyen.
- La API recibe y devuelve el monto como un entero en esa unidad.
- Postgres lo guarda como `BIGINT`.
- El monto siempre viaja con su moneda (código ISO 4217). Qué monedas se aceptan se define en las reglas de negocio.

## Consecuencias

**Positivas**
- Aritmética y comparaciones exactas, por ejemplo al validar el monto máximo.
- No se necesitan dependencias para manejar decimales.

**Negativas**
- El comercio tiene que enviar unidades mínimas: USD 10,50 se envía como `1050`.
- Para mostrar un monto hay que conocer los decimales de cada moneda.

## Alternativas consideradas

- **`float64`**: errores de redondeo.
- **`NUMERIC` en Postgres + una librería decimal en Go**: es exacto, pero agrega una dependencia y conversiones que un pago no necesita.
- **String decimal en la API**: es exacto, pero obliga a parsear y validar el formato en cada request.
