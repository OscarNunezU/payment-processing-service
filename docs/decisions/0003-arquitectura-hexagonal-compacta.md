---
title: "ADR 0003: Arquitectura hexagonal compacta"
status: accepted
date: 2026-10-14
deciders: oscar
---

# ADR 0003: Arquitectura hexagonal compacta

## Contexto

El servicio tiene una entidad principal (la transacción), una entrada (HTTP) y dos salidas (Postgres y el adquirente). Es un dominio angosto pero profundo: la complejidad no está en la cantidad de entidades, sino en el comportamiento (idempotencia, estados, errores temporales y reintentos).

## Decisión

Aplicar puertos y adaptadores, con un paquete por tema:

```
internal/
  payment/    núcleo: estados, reglas, idempotencia, reintentos
  api/        adaptador de entrada (HTTP)
  postgres/   adaptador de salida (persistencia)
  acquirer/   adaptador de salida (cliente del adquirente)
cmd/server/   conecta las piezas
```

- El núcleo no importa HTTP ni Postgres: depende de interfaces que define él mismo (`payment/ports.go`).
- Los adaptadores implementan esas interfaces.
- Cada requisito del enunciado tiene su archivo en `payment/` (`status.go`, `rules.go`, `idempotency.go`...).

## Consecuencias

**Positivas**
- Las reglas se prueban con dobles en memoria, sin base de datos ni red.
- Cambiar el adquirente o la base de datos no toca el núcleo.
- Un revisor encuentra cada requisito por el nombre del archivo.

**Negativas**
- Como todo el núcleo está en un paquete, la separación entre dominio y casos de uso es por archivo y no por paquete: el compilador no la impone.

## Alternativas consideradas

- **Capas planas** (la lógica dentro de los handlers): las reglas no se pueden probar sin HTTP ni base de datos.
- **Clean Architecture completa** (`domain/`, `application/`, `infrastructure/` e `interfaces/`, con DTOs y mappers entre capas): ordena bien sistemas anchos, con muchas entidades e integraciones. Aquí agregaría paquetes y traducciones sin aportar.

## Cuándo revisarla

Pasar a la separación completa en paquetes si aparecen:

- más entidades con lógica propia (reembolsos, comercios, liquidaciones),
- varios adquirentes con enrutamiento,
- más de una entrada al núcleo (por ejemplo, HTTP y un worker que consume una cola),
- varios equipos trabajando en paralelo.

Ese cambio es mover archivos, no reescribir lógica.
