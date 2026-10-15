---
title: "ADR 0006: Estrategia de tests"
status: accepted
date: 2026-10-15
deciders: oscar
---

# ADR 0006: Estrategia de tests

## Contexto

El enunciado pide `go test` y permite sumar testify u otra librería. El núcleo se puede probar sin infraestructura, pero el repositorio necesita un Postgres real: un doble en memoria no prueba las consultas, los constraints ni la concurrencia.

## Decisión

- Solo el paquete `testing` de la librería estándar, con tests de tabla. Sin librerías de aserciones.
- El núcleo se prueba con dobles en memoria de sus puertos.
- Los tests de Postgres corren contra una base real y se saltan si no hay `DATABASE_URL`:
  - `make ci` corre formato, vet y tests sin necesitar Docker.
  - `make test-integration` levanta el Postgres de compose, crea la base `payments_test` y corre todos los tests.
  - El CI levanta un Postgres como servicio del workflow y corre todos los tests.
- Cada test usa sus propios datos (un comercio único), así los tests no se pisan aunque corran en paralelo.

## Consecuencias

**Positivas**
- Sin dependencias nuevas para los tests.
- Los tests unitarios corren en segundos y sin Docker.
- Las consultas, los constraints y la concurrencia se prueban contra el motor real.

**Negativas**
- En local hay que correr `make test-integration` para incluir los tests de Postgres; el CI siempre los corre.
- Los mensajes de error de los tests se escriben a mano.

## Alternativas consideradas

- **testify**: aserciones más cortas, pero suma una dependencia y aquí no ahorra nada importante.
- **testcontainers-go**: cada test levanta su propio Postgres. Es cómodo, pero agrega una dependencia pesada y obliga a tener Docker incluso para los tests unitarios.
