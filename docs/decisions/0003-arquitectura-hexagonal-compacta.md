---
title: "ADR 0003: Arquitectura hexagonal compacta"
status: accepted
date: 2026-10-14
deciders: oscar
---

# ADR 0003: Arquitectura hexagonal compacta

> **Actualización 2026-10-15**: se revisó por la aparición del worker (segunda entrada al núcleo, ADR 0005) y se mantiene la versión compacta. Además, el código se agrupa en `internal/core/` e `internal/adapters/`. El detalle está al final, en "Actualización (2026-10-15)".

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

## Actualización (2026-10-15)

**Revisión por el worker.** El procesamiento asíncrono (ADR 0005) agrega una segunda entrada al núcleo: el worker. Es una de las señales de "Cuándo revisarla". Se revisó y se mantiene la versión compacta: el núcleo sigue siendo un solo dominio con pocos casos de uso, y el worker es un adaptador de entrada más que llama al mismo núcleo.

**Agrupación en carpetas.** El código se agrupa en `internal/core/` (el núcleo) e `internal/adapters/` (entradas y salidas), para que el rol de cada paquete se vea en el árbol y sea más fácil traspasar el conocimiento al equipo. El núcleo sigue siendo un solo paquete (`payment`), sin separar dominio, casos de uso y puertos en paquetes distintos. Las reglas de dependencia no cambian.

```
internal/
  core/
    payment/   núcleo: dominio, casos de uso y puertos, separados por archivo
  adapters/    entradas (api, worker) y salidas (postgres, acquirer, vault)
  config/      configuración
  logging/     logs
cmd/server/    conecta las piezas
```
