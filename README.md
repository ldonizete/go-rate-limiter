# Rate Limiter em Go

Middleware HTTP de rate limiting em Go, com limite por **IP** ou por **token de acesso** (`API_KEY`) e persistência em **Redis**. A persistência é um *Strategy*: o Redis pode ser trocado por outro mecanismo implementando uma única interface.

## Como funciona

| Situação | Regra aplicada |
|---|---|
| Requisição **sem** header `API_KEY` | Limite por IP (`RATE_LIMIT_IP` req/s) |
| Requisição **com** `API_KEY: <TOKEN>` | Limite do token (`TOKEN_LIMITS`, ou `RATE_LIMIT_TOKEN_DEFAULT` se o token não estiver listado). **O limite do IP é ignorado.** |
| Limite excedido | `429 Too Many Requests` com o corpo `you have reached the maximum number of requests or actions allowed within a certain time frame` |
| Depois do 429 | O IP ou token fica bloqueado por `BLOCK_DURATION`; toda requisição nesse período recebe 429 |

**Precedência (Token > IP):** com `RATE_LIMIT_IP=10` e um token com limite 100, esse token pode fazer 100 req/s, mesmo que o IP dele já tenha passado de 10. Token e IP usam contadores e bloqueios independentes.

**Contagem:** janela fixa de 1 segundo. Ao exceder o limite na janela, a chave é bloqueada por `BLOCK_DURATION` (as requisições bloqueadas não renovam o bloqueio).

## Executar

Só é preciso Docker e Docker Compose.

```bash
docker compose up --build
```

Sobe a aplicação em `http://localhost:8080` e o Redis. Para experimentar:

```bash
# Sem token: limite do IP (10 req/s por padrão). A 11ª em 1s recebe 429.
for i in $(seq 1 12); do curl -s -o /dev/null -w "%{http_code} " localhost:8080/; done

# Com token: vale o limite do token (vip = 500 req/s no .env de exemplo).
curl -i -H "API_KEY: vip" localhost:8080/

# Ver a resposta de bloqueio completa.
curl -i localhost:8080/
```

Para encerrar: `docker compose down`.

## Testes

```bash
docker compose run --rm tests
```

Executa `go test -race ./...` dentro de um container, com o Redis do compose. Não precisa de Go instalado.

O que é coberto:

- **Eficácia do limitador:** limite por IP e por token, janela que reinicia, bloqueio que dura `BLOCK_DURATION` (mesmo depois de a janela de 1s acabar), isolamento entre clientes, 200 requisições concorrentes com exatamente 10 permitidas.
- **Precedência Token > IP**, no limiter, no middleware (HTTP) e contra o Redis real:
  - token com limite maior que o IP continua passando depois que o IP passaria a ser bloqueado;
  - token com limite menor que o IP é bloqueado pelo limite do token;
  - IP bloqueado não impede um token de passar.
- **Middleware:** status 429, corpo exato, IP por `RemoteAddr` (ignorando a porta), `X-Forwarded-For` só quando confiável e comportamento em falha do store.
- **Redis:** atomicidade (100 goroutines), expiração da janela e do bloqueio.

Os testes de integração com Redis são ignorados (`skip`) quando `REDIS_ADDR` não está definido. Com Go instalado, `go test ./...` roda os testes que não dependem do Redis.

## Configuração

Tudo é configurado por variáveis de ambiente, ou pelo arquivo [`.env`](.env) na raiz (variáveis de ambiente reais têm prioridade sobre o arquivo). O `docker-compose.yaml` carrega o `.env` e sobrescreve apenas `REDIS_ADDR=redis:6379`.

| Variável | Padrão | Descrição |
|---|---|---|
| `SERVER_PORT` | `8080` | Porta do servidor HTTP |
| `STORE_DRIVER` | `redis` | Estratégia de persistência: `redis` ou `memory` |
| `RATE_LIMIT_IP` | `10` | Máximo de req/s por IP |
| `RATE_LIMIT_TOKEN_DEFAULT` | `100` | Máximo de req/s para tokens que não estão em `TOKEN_LIMITS` |
| `TOKEN_LIMITS` | vazio | Limites por token: `token1:100,token2:500` |
| `BLOCK_DURATION` | `5m` | Tempo de bloqueio após exceder o limite (`30s`, `5m`, `1h`) |
| `REDIS_ADDR` | `localhost:6379` | Endereço do Redis |
| `REDIS_PASSWORD` | vazio | Senha do Redis |
| `REDIS_DB` | `0` | Banco do Redis |
| `FAIL_OPEN` | `true` | Se o store falhar: `true` deixa a requisição passar, `false` responde 503 |
| `TRUST_PROXY_HEADERS` | `false` | `true` usa o primeiro IP de `X-Forwarded-For` (somente atrás de um proxy confiável) |

Valores inválidos (limite zero, duração mal formatada etc.) fazem a aplicação falhar na inicialização com uma mensagem clara.

Para alterar os limites com Docker Compose, edite o `.env` e suba de novo (`docker compose up --build`). Rodando fora do Docker (`go run ./cmd/server`), variáveis de ambiente do shell têm prioridade sobre o `.env`.

## Como trocar a estratégia de persistência

A lógica de limitação (`internal/limiter`) depende apenas desta interface:

```go
// internal/limiter/store.go
type Store interface {
    Hit(ctx context.Context, key string, limit int, window, blockFor time.Duration) (allowed bool, err error)
}
```

`Hit` registra uma requisição para `key` e informa se ela é permitida. Deve ser **atômico**:

1. se `key` está bloqueada, retorna `false` sem contar;
2. senão, conta a requisição numa janela fixa de tamanho `window`;
3. se a contagem passa de `limit`, bloqueia `key` por `blockFor` e retorna `false`.

Qual chave e qual limite usar (a regra Token > IP) é decidido pelo `Limiter`, não pelo `Store`. Por isso uma nova estratégia não precisa conhecer IPs nem tokens.

Para adicionar um novo backend (por exemplo Memcached ou um banco SQL):

1. Crie `internal/store/<nome>/` com um tipo que implemente `limiter.Store`.
2. Registre-o em `newStore` ([`cmd/server/main.go`](cmd/server/main.go)):
   ```go
   case "meubackend":
       return meubackend.New(...), func() { /* fechar conexões */ }, nil
   ```
3. Use `STORE_DRIVER=meubackend`.

Implementações existentes para servir de referência:

- [`internal/store/redisstore`](internal/store/redisstore/redisstore.go): script Lua, uma ida ao Redis por requisição, sem condição de corrida entre contar e bloquear.
- [`internal/store/memory`](internal/store/memory/memory.go): em memória, com relógio injetável (usado nos testes).

## Estrutura

```
cmd/server/            composition root: config, escolha do store, rotas, graceful shutdown
internal/config/       leitura e validação de variáveis de ambiente / .env
internal/limiter/      regras de negócio e interface Store (sem net/http)
internal/middleware/   adaptador HTTP: extrai IP e API_KEY, escreve o 429
internal/store/memory/      Store em memória
internal/store/redisstore/  Store em Redis
```

O middleware depende de uma interface mínima (`Allow(ctx, ip, token) (bool, error)`), então a lógica de negócio e a camada HTTP são testáveis separadamente.

## Decisões e limitações

- **Janela fixa:** em teoria um cliente pode enviar até 2× o limite na virada de dois segundos consecutivos. É o comportamento esperado de "N requisições por segundo" com janela fixa. Se isso importar, a evolução natural é uma janela deslizante, trocando só a implementação do `Store`.
- **Tokens desconhecidos:** qualquer valor em `API_KEY` recebe um limite próprio (`RATE_LIMIT_TOKEN_DEFAULT`) em vez de cair no limite do IP, porque a especificação define que o token se sobrepõe ao IP. Em produção, valide os tokens (por exemplo, rejeitando os não cadastrados) antes de aplicar o limite, para que ninguém contorne o limite do IP enviando tokens aleatórios.
- **Falha do Redis:** por padrão (`FAIL_OPEN=true`) o serviço continua atendendo e registra o erro no log. Use `FAIL_OPEN=false` para responder 503.
- **IP do cliente:** vem de `RemoteAddr`. Atrás de um proxy ou load balancer, ative `TRUST_PROXY_HEADERS=true` somente se o proxy sobrescrever `X-Forwarded-For`, senão o cliente pode forjar o header e fugir do limite.
- **Nome do header:** `API_KEY` contém underscore. Alguns proxies (como o nginx, por padrão) descartam headers com underscore; nesse caso ajuste o proxy (`underscores_in_headers on;`).
