---
title: "ADR 0011: Front con Next.js"
status: accepted
date: 2026-10-16
deciders: oscar
---

# ADR 0011: Front con Next.js

## Contexto

El enunciado deja como opcional una vista simple para consultar transacciones, con React, Next.js 15 o superior y TypeScript. Sin ella, ver un pago y su historia exige `curl`, y lo que pasa en segundo plano, como los reintentos, no se ve.

## Decisión

- **Next.js 16 con App Router y TypeScript, sobre Node 22.** El equipo declara la 15, pero la 15 deja de tener soporte el 21 de octubre de 2026 (la política de Next.js le da dos años a cada versión mayor). La 16 es la LTS activa, y el código no usa nada exclusivo de ella. Node 22 es la LTS que sigue a la 20. Las versiones van fijas, y el CI corre `npm audit`, como `govulncheck` en el backend.
- **Las páginas son Server Components que consultan la API desde el servidor de Next.** El navegador nunca habla con la API, así que no hace falta CORS. `lib/api.ts`, el único módulo que conoce la API, importa `server-only`: el build falla si un componente del navegador lo usa. Nada se guarda en caché, porque los pagos cambian mientras el worker los procesa.
- **El listado filtra por comercio y estado con un formulario GET (`next/form`)**: los filtros quedan en la URL y funcionan sin JavaScript. La tabla va en un `Suspense` con una `key` por filtro, como en el tutorial oficial de Next.js para búsquedas, y el formulario lleva su propia `key` para mostrar siempre los filtros de la URL.
- **El detalle se actualiza solo mientras el pago está en vuelo**: le pide la página al servidor cada segundo (`router.refresh()`) y se detiene en un estado final.
- **El pago de prueba es una Server Action.** La clave de idempotencia se genera al mostrar el formulario, y la página se renderiza en cada visita para que cada una tenga la suya: si el mismo formulario llega dos veces, la API devuelve el mismo pago. Si la API rechaza el pago o no responde, el formulario lo dice y conserva lo enviado y la clave, así que corregir o reenviar nunca duplica un pago.
- **Diseño propio con CSS Modules y la tipografía Geist**, sin librerías de componentes. El tema (claro, oscuro o el del sistema) va en una cookie y no en `localStorage`, como en next-themes: el servidor entrega el HTML con el tema correcto y no hay parpadeo. Un pago que no existe responde 404, y si la API no responde, "Reintentar" vuelve a pedir la página.
- **Tests de la lógica pura** con el runner de Node (`node --test`), sin dependencias, como el backend (ADR 0006). El build chequea los tipos.
- **Imagen con la salida `standalone` de Next** sobre `distroless/nodejs22-debian12:nonroot`: sin shell ni usuario root, y con los módulos que traza Next en vez de todo `node_modules`.

## Consecuencias

**Positivas**
- La demo no depende de `curl`: se crea un pago y se ve cómo avanza, intento por intento.
- El front usa la API como cualquier cliente, sin endpoints propios, y la API no queda expuesta al navegador.
- Cinco dependencias de producción: `next`, `react`, `react-dom`, `geist` y `server-only`.

**Negativas**
- Cada consulta da un salto más: navegador, Next y API.
- Mientras un pago está en vuelo, cada pestaña abierta en su detalle hace dos requests por segundo a la API: sirve para una consola de pruebas, no para muchos usuarios mirando a la vez.
- Los tests automatizados cubren solo la lógica pura; los flujos no tienen tests de punta a punta.
- No es la versión que declara el equipo: la 16 compila con Turbopack y trae React 19.2, aunque en esta vista no cambia nada visible.
- La imagen pesa 77 MB comprimida y 230 MB en disco, e incluye módulos que esta vista no usa, como `sharp` (46 MB).
- El front no tiene login y lista los pagos de todos los comercios, igual que `GET /payments`.

## Alternativas consideradas

- **Next.js 15**: deja de tener soporte el 21 de octubre de 2026, y su última versión fija un PostCSS con cuatro avisos de seguridad.
- **Consultar la API desde el navegador**: obliga a habilitar CORS y a exponer la API.
- **Tailwind o una librería de componentes**: para tres páginas suman dependencias y configuración; CSS Modules viene con Next.
- **Un `loading.tsx`**: no se vuelve a mostrar cuando solo cambian los parámetros de búsqueda, y en Next 15.5, puesto en la raíz, dejaba esas navegaciones sin aplicar. En el detalle, el streaming haría que un pago que no existe respondiera 200 en vez de 404.
- **Server-Sent Events o WebSockets**: la API tendría que publicar los cambios; el refresco reutiliza los endpoints que ya existen.

## Evolución

- Login por comercio, para que cada uno vea solo sus pagos.
- Paginación por cursor en el listado.
- El estado en vivo con Server-Sent Events.
- Tests de punta a punta con Playwright en el CI.
- Pasar a Node 24 antes de abril de 2027.
