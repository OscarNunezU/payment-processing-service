---
title: "ADR 0011: Front con Next.js"
status: accepted
date: 2026-10-16
deciders: oscar
---

# ADR 0011: Front con Next.js

## Contexto

El enunciado deja como opcional una vista simple para consultar transacciones, con React, Next.js 15 o superior y TypeScript. Sin ella, para ver un pago y su historia hay que usar `curl`, y lo que pasa en segundo plano, como los reintentos, no se ve.

## Decisión

- **Next.js 16.4 con App Router, React 19.2 y TypeScript, sobre Node 22.**
  - Next 16 cumple el "15 o superior" del enunciado. El equipo declara la 15, pero la 15 deja de tener soporte el 21 de octubre de 2026: la política de Next.js le da dos años a cada versión mayor. La 16 es la LTS activa.
  - El código no usa nada exclusivo de la 16.
  - Node 22 es la LTS que sigue a la 20 que declara el equipo; la 20 dejó de tener soporte el 30 de abril de 2026.
  - Las versiones van fijas: las dependencias en `package.json`, y Node 22.23.2 en `web/.nvmrc` (local y CI) y en el Dockerfile. El CI corre `npm audit` sobre las dependencias de producción, como `govulncheck` en el backend.
- **Las páginas son Server Components que consultan la API desde el servidor de Next.**
  - El navegador solo habla con Next, y Next llama a la API (`API_URL`). La API no necesita CORS ni ser alcanzable desde el navegador.
  - `lib/api.ts` es el único módulo que conoce el contrato HTTP de la API. Importa `server-only`, así que el build falla si un componente del navegador lo usa, y `API_URL` no llega al navegador.
  - Nada se guarda en caché (`cache: "no-store"`), porque los pagos cambian mientras el worker los procesa.
- **El listado** muestra los 50 pagos más recientes y filtra por comercio y estado, los mismos parámetros de `GET /payments`.
  - Los filtros son un formulario GET (`next/form`) y quedan en la URL: un listado filtrado se puede compartir, y el formulario funciona antes de que cargue JavaScript.
  - La tabla va dentro de un `Suspense` con una `key` por filtro, como en el tutorial oficial de Next.js para búsquedas: cada filtro nuevo muestra un esqueleto mientras la API responde.
  - El formulario lleva su propia `key`, así que siempre muestra los filtros de la URL, también al volver atrás o al limpiar.
- **El detalle se actualiza solo mientras el pago está en vuelo.** Un componente del navegador le pide al servidor que vuelva a renderizar la página cada segundo (`router.refresh()`) y se detiene cuando el estado es final. La historia viene de `GET /payments/{id}/events` y muestra cada intento con su motivo.
- **El pago de prueba es una Server Action.**
  - El formulario se envía al servidor de Next, que llama a `POST /payments`.
  - La clave de idempotencia se genera en el servidor al mostrar el formulario y viaja en un campo oculto, así que si el mismo formulario llega dos veces, la API devuelve el mismo pago. Por eso la página se renderiza en cada visita: una estática le daría la misma clave a todos.
  - Si la API rechaza el pago, el formulario muestra su código, su mensaje y los campos con error, junto a lo que se envió. Como la API no guarda las requests rechazadas, se corrige y se reenvía con la misma clave.
  - Si la API no responde o falla, el formulario lo dice y conserva lo enviado y la clave: el pago pudo crearse o no, y reenviarlo es seguro en los dos casos.
  - Las reglas viven en la API; el formulario solo exige los campos y un monto positivo.
- **Diseño propio con CSS Modules**, sin librerías de componentes:
  - Variables de diseño en `globals.css` (colores, radios y sombras) y la tipografía Geist, que Next sirve desde el mismo servidor (`next/font`).
  - Cada estado tiene su color, y el punto de los pagos en vuelo pulsa, salvo con `prefers-reduced-motion`.
  - En pantallas chicas, la tabla oculta las columnas secundarias y el detalle pasa a una columna. En la historia, los chips bajan bajo el estado cuando la tarjeta es angosta: una container query mide la tarjeta, no la pantalla.
  - `error.tsx` y `not-found.tsx` cubren los errores y los 404: un pago que no existe responde 404 con su propio mensaje. Si la API no responde, la página lo dice, y "Reintentar" le vuelve a pedir la página al servidor.
- **Tema claro, oscuro o el del sistema, guardado en una cookie.** El servidor lee la cookie y entrega el HTML con el tema elegido, así que no hay parpadeo al cargar. "Sistema" borra la cookie y deja que decida `prefers-color-scheme`.
- **Tests de la lógica pura** (los formatos de montos y fechas, y los estados) con el runner de Node (`node --test`), que ejecuta TypeScript sin compilarlo. Sin dependencias, como el backend (ADR 0006). El build de Next chequea los tipos.
- **Imagen con la salida `standalone` de Next**, que trae su propio servidor y copia solo los módulos que traza, en vez de todo `node_modules`. El build corre en `node:22.23.2-bookworm-slim` y la imagen final es `distroless/nodejs22-debian12:nonroot`, sin shell y sin usuario root.

## Consecuencias

**Positivas**
- La demo no depende de `curl`: se crea un pago y se ve cómo avanza, intento por intento.
- El front usa la API como cualquier cliente, sin endpoints propios: muestra lo que la API responde.
- La API no necesita CORS ni quedar expuesta al navegador.
- Cinco dependencias de producción: `next`, `react`, `react-dom`, `geist` y `server-only`.

**Negativas**
- Cada consulta da un salto más: navegador, Next y API.
- Mientras un pago está en vuelo, cada pestaña abierta en su detalle hace dos requests por segundo a la API. Sirve para una consola de pruebas; con muchos usuarios mirando, convendría que la API avisara los cambios.
- Los tests automatizados cubren solo la lógica pura; los flujos no tienen tests de punta a punta.
- No es la versión que declara el equipo: la 16 compila con Turbopack en vez de webpack y trae React 19.2. En esta vista no cambia nada visible.
- La imagen pesa 77 MB comprimida y 230 MB en disco: 121 MB son el runtime de Node y 70 MB la app, que incluye módulos que esta vista no usa, como `sharp` (46 MB), con el que Next optimiza imágenes.
- Node 22 tiene soporte hasta abril de 2027; antes hay que pasar a Node 24.
- El front no tiene login y lista los pagos de todos los comercios, igual que `GET /payments`.

## Alternativas consideradas

- **Next.js 15, la que declara el equipo**: deja de tener soporte el 21 de octubre de 2026, y su última versión fija un PostCSS con cuatro avisos de seguridad, que había que corregir con un override.
- **Consultar la API desde el navegador**: obliga a habilitar CORS y a que el navegador alcance la API. Desde el servidor de Next, la API puede quedar en la red interna.
- **Tailwind o una librería de componentes, como shadcn/ui o MUI**: aceleran las interfaces grandes, pero para tres páginas suman dependencias y configuración. CSS Modules viene con Next.
- **next-themes**: guarda el tema en `localStorage` y lo aplica con un script antes de pintar. Con una cookie, el servidor entrega el tema correcto sin scripts ni dependencias.
- **Un `loading.tsx`**:
  - En el listado no se vuelve a mostrar cuando solo cambian los parámetros de búsqueda. En Next 15.5, además, uno en la raíz dejaba sin aplicar esas navegaciones: ni la URL ni la lista cambiaban. En la 16.4 eso ya no pasa.
  - En el detalle, el streaming haría que un pago que no existe respondiera 200 en vez de 404, porque los headers salen junto con el esqueleto.
- **Server-Sent Events o WebSockets para el estado en vivo**: la API tendría que publicar los cambios. El refresco reutiliza los endpoints que ya existen y se detiene solo.
- **Una SPA con Vite**: el enunciado propone Next.js, y una SPA consultaría la API desde el navegador.
- **Vitest o Jest**: Node ya trae un runner de tests que ejecuta TypeScript, y para funciones puras alcanza.

## Evolución

- Login por comercio, para que cada uno vea solo sus pagos.
- Paginación por cursor en el listado.
- El estado en vivo con Server-Sent Events, cuando la API publique los cambios.
- Tests de punta a punta con Playwright en el CI.
- Pasar a Node 24 antes de abril de 2027.
