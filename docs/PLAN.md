---
PLAN: "feat!: d1 habla la Sessions API — las lecturas dejan de cruzar el hemisferio"
EXECUTOR: jules
REVIEWER: none
STATUS: review
SESSION: 6771035188629774588
PR: https://github.com/tinywasm/cloudflare/pull/2
---

> This plan is dispatched via the CodeJob workflow. See skill: agents-workflow.

# PLAN — Sessions API de D1 en `d1/`

> ⚠️ **Lee la sección "El problema de ciclo de vida" antes de escribir código.**
> Contiene la única decisión abierta de este plan y la etapa 0 existe para
> cerrarla con una medición, no con una opinión.

## Por qué

`veltylabs/iam` responde `/api/health` en 163-363 ms. Medido contra la base real:

```
D1 velty-iam-db → running_in_region: ENAM      read_replication: disabled
consulta de prueba → served_by_colo: ATL, sql_duration_ms: 1.05
```

El Worker corre en Santiago, la base en Norteamérica Este. **El SQL cuesta 1 ms;
el resto es el viaje.**

Cloudflare resuelve esto con réplicas de lectura: copias de solo lectura que
crea y enruta sola, sin costo extra, cerca de quien consulta. Pero hay una
condición dura, textual en su documentación:

> To use read replication, you must use the D1 Sessions API, otherwise all
> queries will continue to be executed only by the primary database.

Activar las réplicas sin la Sessions API **no cambia absolutamente nada**. Por
eso este plan existe: el adaptador de `d1/` habla hoy directo contra el binding,
así que hoy todas las consultas van a la primaria por definición.

## Cómo funciona la Sessions API

```js
const session = env.DB.withSession(bookmark);   // bookmark: string
const result  = await session.prepare(sql).bind(...).run();
const next    = session.getBookmark();          // string, para la siguiente sesión
```

Un *bookmark* es un marcador de versión. `withSession(b)` garantiza que se lee
una versión de la base **al menos tan fresca como `b`**, aunque la sirva una
réplica distinta. Eso es lo que da consistencia secuencial: sin él, dos
consultas seguidas podrían caer en réplicas con retrasos distintos y la segunda
ver menos que la primera.

Dos valores especiales:

| Valor | Significado |
|---|---|
| `"first-unconstrained"` | primera consulta a cualquier instancia, réplica incluida — lo más rápido |
| `"first-primary"` | primera consulta forzada a la primaria — lo más fresco |

## El problema de ciclo de vida — la decisión abierta

Hoy [`d1/adapter.go`](../d1/adapter.go) se construye **una vez por isolate**:

```go
func NewEdge(bindingName string) (*orm.DB, error) {
	v := js.Global().Get("context").Get("env").Get(bindingName)
	...
	return orm.New(&adapter{dbObj: v, Compiler: sqlt.NewCompiler()}), nil
}
```

`iam` lo llama en `main()`, y ese `*orm.DB` lo comparten **todas** las peticiones
del isolate. Pero una sesión de D1 es **por petición**. Ahí está el desajuste.

Y hay un camino que **está prohibido tomar**: guardar la sesión en una variable
a nivel de paquete y mutarla al empezar cada petición. Varias peticiones pueden
estar en vuelo a la vez dentro de un mismo isolate mientras esperan promesas.
Ese patrón exacto —estado compartido entre peticiones concurrentes en un
isolate— ya costó horas de diagnóstico en este repo con `globalThis.workers`, y
se resolvió estableciendo que `context.binding` es el único canal seguro por
instancia. **No lo repitas.**

Tampoco sirve el almacén por petición que ya tiene `edge`: desde el commit
`90978f7`, `wasmContext.SetValue(key, value string)` es **solo de cadenas** y no
puede llevar un `js.Value`.

### La salida: el bookmark es una cadena

Un `js.Value` no cabe en el almacén por petición; **un bookmark sí**, porque es
texto. Así que en vez de guardar la sesión, se guarda su bookmark y se
reconstruye la sesión por sentencia, encadenada:

```
withSession(bookmarkActual) → prepare/run → getBookmark() → guardar como bookmarkActual
```

Encadenar bookmarks entre sentencias es el **mismo** mecanismo que Cloudflare
documenta para encadenarlos entre peticiones. La consistencia secuencial se
mantiene, y el estado que viaja es una cadena por petición, no un objeto
compartido.

## Etapa 0 — el spike, antes de cualquier otra cosa

**No implementes las etapas 1-3 hasta cerrar ésta.** Todo lo de arriba es
razonamiento sobre documentación; esto lo confirma contra la plataforma real.

1. Activa las réplicas en una base D1 **de pruebas** (no `velty-iam-db`):
   ```sh
   curl -X PUT "https://api.cloudflare.com/client/v4/accounts/{account_id}/d1/database/{test_db_id}" \
     -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
     -d '{"read_replication": {"mode": "auto"}}'
   ```
2. Despliega un Worker mínimo que haga, en una sola petición: `withSession("first-unconstrained")`
   → `SELECT` → `getBookmark()` → `withSession(bookmark)` → otro `SELECT` →
   `getBookmark()`.
3. Registra, de la respuesta de D1, los campos `meta.served_by_region`,
   `meta.served_by_primary` y `meta.duration` de **cada** consulta.

**Reporta la tabla en el PR.** Lo que hay que ver: la primera consulta servida
por una réplica (`served_by_primary: false`) con una `duration` claramente menor,
y la segunda respetando el bookmark.

> Si la etapa 0 muestra que las réplicas no llegan a la región de este tráfico,
> o que la ganancia no compensa, **detente y repórtalo**. Cerrar el plan sin
> ganancia medida no sirve de nada, y el resto de las etapas serían complejidad
> pura.

## Etapa 1 — el mecanismo en `d1/`

Archivo nuevo **`d1/session.go`** (`//go:build wasm`).

```go
const (
	// BookmarkFirstUnconstrained abre la sesion contra cualquier instancia,
	// replica incluida. Es lo mas rapido y lo correcto para lecturas que
	// toleran el retraso de replicacion.
	BookmarkFirstUnconstrained = "first-unconstrained"

	// BookmarkFirstPrimary fuerza la primera consulta contra la primaria. Es
	// lo correcto cuando la peticion va a escribir, o cuando debe ver una
	// escritura hecha en otra peticion sin bookmark que las enlace.
	BookmarkFirstPrimary = "first-primary"
)

// BookmarkStore lleva el bookmark de D1 de una sentencia a la siguiente dentro
// de una misma peticion. La implementacion la inyecta el consumidor y DEBE ser
// por peticion: varias peticiones pueden estar en vuelo a la vez en un mismo
// isolate mientras esperan promesas, asi que un almacen compartido entre ellas
// mezclaria versiones de la base entre usuarios distintos.
type BookmarkStore interface {
	Bookmark() string
	SetBookmark(string)
}
```

Y el adaptador gana el almacén, opcional:

```go
// NewEdgeSession abre el binding igual que NewEdge, pero enruta cada sentencia
// por la Sessions API de D1 usando store para encadenar bookmarks. Sin esto,
// las replicas de lectura no se usan aunque esten activadas.
func NewEdgeSession(bindingName string, store BookmarkStore) (*orm.DB, error)
```

`NewEdge` **se queda exactamente como está** y sigue yendo a la primaria. Es el
comportamiento correcto para la migración de esquema y para cualquier consumidor
que no quiera pensar en replicación.

En `adapter`, extrae el `a.dbObj.Call("prepare", query)` que hoy está repetido
en `Exec`, `QueryRow` y `Query` a un solo método:

```go
// stmt prepara query contra la sesion vigente. Cuando no hay store, prepara
// directo contra el binding y el comportamiento es el de siempre.
func (a *adapter) stmt(query string) js.Value
```

y un cierre que se llama **después** de cada operación:

```go
// captureBookmark guarda el bookmark de la sesion que acaba de ejecutar, para
// que la siguiente sentencia de esta peticion lea una version al menos igual
// de fresca.
func (a *adapter) captureBookmark(sess js.Value)
```

`captureBookmark` no hace nada si no hay store. Con store, `sess.Call("getBookmark")`
puede devolver `null`: comprueba `IsNull()`/`IsUndefined()` antes de `.String()`
o escribirás la cadena `"<null>"` como bookmark y la siguiente sentencia fallará.

## Etapa 2 — un `BookmarkStore` por petición en `edge/`

`edge` ya tiene almacén de cadenas por petición. Expón encima de él:

```go
// BookmarkStore devuelve un d1.BookmarkStore respaldado por el almacen de
// cadenas por peticion de ctx. Es el enlace entre el router y el adaptador de
// D1: cada peticion lleva el suyo, asi que dos peticiones concurrentes en el
// mismo isolate nunca comparten version de la base.
func BookmarkStore(ctx router.Context) d1.BookmarkStore
```

Clave del almacén: una constante, `const bookmarkKey = "d1_bookmark"`, no un
literal repartido.

> ⚠️ **`edge` no puede importar `d1` si `d1` importa `edge`.** Comprueba la
> dirección antes de escribir: si crea un ciclo, la interfaz `BookmarkStore` se
> declara en `d1` (que no importa `edge`) y `edge` la implementa. Ése es el
> sentido correcto, y es como está escrita arriba.

## Etapa 3 — documentación

- **`docs/D1.md`**: sección nueva sobre réplicas de lectura. Debe decir, con
  todas las letras, que **activar `read_replication: auto` sin usar la Sessions
  API no cambia nada**, porque es el malentendido que hace perder una tarde.
  Incluye la tabla de la etapa 0 con las cifras medidas.
- **`AGENTS.md`**: en las invariantes del runtime, añade que el estado por
  petición nunca vive en variables de paquete, con el precedente de
  `globalThis.workers`.
- **`README.md`**: reindexa.

## Criterios de aceptación

- `grep -rn "^var \|^	[a-zA-Z]* js.Value$" d1/*.go` → ninguna variable de
  paquete que guarde estado por petición.
- `grep -c 'Call("prepare"' d1/adapter.go` → **1** (hoy son 3).
- `NewEdge` sigue existiendo con la misma firma y sin store.
- `GOOS=js GOARCH=wasm go build ./...` limpio.
- `gotest ./...` en verde.
- La tabla de la etapa 0, con cifras reales, está en el PR y en `docs/D1.md`.

## Tests

`d1/` es código `wasm` que habla con `js.Global()`, así que se prueba en un
navegador con un `context.env` falso, como el resto del repo — ver
`docs/TESTING.md`. El falso debe exponer `withSession` devolviendo un objeto con
`prepare` y `getBookmark`.

1. `TestNewEdgeDoesNotUseSessions` — con el falso, `NewEdge` nunca llama a
   `withSession`. Protege el camino de la migración de esquema.
2. `TestSessionChainsBookmarks` — dos `Query` seguidos sobre un `NewEdgeSession`:
   el primero pasa `first-unconstrained`, el segundo pasa el bookmark que
   devolvió el primero.
3. `TestNullBookmarkIsIgnored` — con `getBookmark()` devolviendo `null`, el
   almacén no se ensucia y la siguiente sentencia vuelve a usar el valor
   inicial.

## Lo que NO hay que hacer

- **No** actives las réplicas en `velty-iam-db` desde este plan. Es un paso de
  infraestructura que va coordinado con el despliegue de `iam`; está en el
  master plan.
- **No** metas política en la librería. Qué rutas leen de réplica y cuáles
  exigen la primaria lo decide el consumidor. `d1` expone el mecanismo.
- **No** toques `d1/rows.go` ni `d1/errors.go`.
