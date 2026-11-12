[English](README.md) | **한국어**

# tello-go

Tello용 Go SDK. `tello.Client`는 WebSocket `/sdk` 프로토콜로 대화의 두뇌를
맡습니다. 게이트웨이는 진행 중인 통화에서 상대방이 말한 턴을 실시간으로
넘겨주고, 핸들러가 만든 답변은 다시 통화로 전달됩니다. `tello.API`는 공개 REST
자원 API(도구·에이전트·크루, 크루 자동 생성, 통화 초안)를 쓰는 별도
클라이언트입니다([7절](#7-rest-자원-api)).

> 저장소: `tello-go` · 모듈: `github.com/tello-ai/tello-go` · 패키지: `tello`
>
> webhook은 제공하지 않습니다. REST API의 통화 엔드포인트(`/v1/calls`: 생성·조회·
> 목록·종료·전환)는 이 SDK에 들어 있지 않습니다.

## 1. 설치

```bash
go get github.com/tello-ai/tello-go/tello
```

Go 1.22 이상이 필요합니다. 런타임 의존성은 `github.com/gorilla/websocket`
하나뿐이고, REST 클라이언트는 `net/http`를 씁니다.

## 2. API 키

`NewClient("")`는 `TELLO_API_KEY`에서 키를 읽습니다. URL은 `TELLO_URL`을 먼저
보고, 없으면 기본값인 운영 게이트웨이 `wss://api.telloai.io/sdk`를 씁니다.
`WithURL`로 직접 지정할 수도 있습니다.

키 인증은 `Connect`가 내부에서 끝냅니다. 소켓이 열리면 `auth` 프레임
(`{"event":"auth","data":{"token":"<apiKey>"}}`)을 보내고, 서버가 `auth.ok`로
응답한 뒤에야 반환합니다. `Authorization` 헤더나 query string 토큰은 쓰지 않고,
키가 로그나 오류 메시지에 남지도 않습니다. 인증에 실패하거나, 서버가 `4401`로
연결을 닫거나, `WithOpenTimeout` 안에 `auth.ok`가 오지 않으면 `Connect`가 오류를
반환합니다. 인증이 끝나기 전에는 어떤 명령도 나가지 않습니다.

## 3. 연결 + 통화 시작

```go
package main

import (
	"context"
	"log"

	"github.com/tello-ai/tello-go/tello"
)

func main() {
	ctx := context.Background()
	client, err := tello.NewClient("")
	if err != nil {
		log.Fatal(err)
	}

	client.On(tello.EventTypeUserTurn, func(ctx context.Context, event tello.Event) error {
		return client.Answer(ctx, "heard: "+event.Text, "", "")
	})
	// DTMF 전송: client.SendDtmf(ctx, "1234#", "", "")

	if err := client.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	if err := client.CreateCall(ctx, "+821012345678", "예약 확인", nil, ""); err != nil {
		log.Fatal(err)
	}
	if err := client.WaitClosed(ctx); err != nil {
		log.Fatal(err)
	}
}
```

핸들러는 `Connect` 전에 등록하세요. 그래야 프레임을 놓치지 않습니다.

## 4. 실시간 턴 이벤트 (pub/sub)

`client.On(eventType string, handler func(context.Context, tello.Event) error)`로
이벤트 타입별 구독을 등록합니다. 모든 이벤트는 `tello.Event` 구조체 하나로
전달되고, 채워지는 필드는 타입마다 다릅니다. 디코딩된 원본 프레임은 언제나
`event.Raw`에 들어 있습니다.

| 상수 | 값 | 채워지는 필드 |
| --- | --- | --- |
| `EventTypeCallCreated` | `call.created` | `CallID`, `SessionID` |
| `EventTypeUserTurn` | `user.turn` | `TurnIndex`, `Text` |
| `EventTypeAgentTurn` | `agent.turn` | `TurnIndex`, `Text` |
| `EventTypeAnswerAccepted` | `answer.accepted` | `RequestID`, `MessageID` |
| `EventTypeDtmfAccepted` | `dtmf.accepted` | `RequestID`, `MessageID`, `Digits` |
| `EventTypeCallSummary` | `call.summary` | `RequestID`, `Status`, `DurationSeconds`, `Transcript`, `Summary`, `CreditCharged` |
| `EventTypeCallStatusChanged` | `call.statusChanged` | `Status`, `PreviousStatus` |
| `EventTypeCallCompleted` | `call.completed` | `Status` |
| `EventTypeCallNoAnswer` | `call.noAnswer` | `Status`, `FailureReason` |
| `EventTypeCallFailed` | `call.failed` | `Status`, `FailureReason` |
| `EventTypeError` | `error` | `Code`, `Message`, `RequestID`, `Question`, `RequiredScope`, `IP` |
| `EventTypeDisconnected` | `disconnected` | SDK 자체 이벤트. WS가 닫힐 때 발생 |

`auth.ok`는 `Connect`가 내부에서 소비하며 밖으로 다시 emit 하지 않습니다.

## 5. 명령

```go
client.CreateCall(ctx, to, prompt string, metadata map[string]any, requestID string) error
client.Answer(ctx, text, messageID, requestID string) error
client.SendDtmf(ctx, digits, messageID, requestID string) error
client.Cancel(ctx) error
client.GetSummary(ctx, callID, requestID string) error
```

선택 항목인 문자열 인자에는 `""`를 넘기면 됩니다. `requestID`는 명령과 응답
프레임을 짝지어 주는 값이며, 멱등성 키가 아닙니다. `CreateCall`은 항상
`requestId`를 보냅니다. 넘긴 값이 비어 있지 않으면 그 값을, 비어 있으면 생성한
UUID를 씁니다. 명령마다 `requestID`를 따로 주고, `CreateCall`에 쓴 값을 다른
명령에 다시 쓰지 마세요. `CreateCall`의 requestId를 되돌려 주는 오류는 대기를
끝냅니다.

`client.WaitClosed(ctx)`는 통화가 종료 상태(`call.completed` / `call.noAnswer` /
`call.failed`, 또는 status가 `cancelled`인 `call.statusChanged`)에 이르거나, 이
통화의 `CreateCall`에 대한 오류가 오거나, 연결이 닫히면 반환합니다. 무한정
기다리지 않으려면 `context.WithTimeout`으로 상한을 거세요.

진행 중인 `WaitClosed`는 자기 통화가 끝나면 반환합니다. 그 통화의 종료 이벤트
핸들러가 후속 통화를 시작해도 마찬가지입니다. 후속 통화를 기다리려면
`WaitClosed`를 다시 호출하세요.

`Cancel`에는 별도 응답이 없습니다. 게이트웨이가 취소를 반영하면 status가
`cancelled`인 `call.statusChanged`(`PreviousStatus`는 취소 직전 상태)를 보내고,
이 프레임이 그 통화의 종료 이벤트입니다.

## 6. 오류 처리

게이트웨이 오류 프레임은 오류 타입과 1:1로 대응됩니다:

| 게이트웨이 `code` | 오류 타입 |
| --- | --- |
| `unauthenticated` | `*AuthenticationError` (인증 핸드셰이크. 4401 종료 포함) |
| `ipNotAllowed` | `*AuthenticationError`, `.IP` 포함 (인증 핸드셰이크. 키의 허용 IP 목록 밖. 4401 종료) |
| `insufficientScope` | `*PermissionDeniedError`, `.RequiredScope` 포함 (그 명령만 거절하고 연결은 유지) |
| `toRequired` | `*ValidationError` |
| `callIdRequired` | `*ValidationError` |
| `dtmfDigitsRequired` | `*ValidationError` |
| `dtmfDigitsInvalid` | `*ValidationError` |
| `callAlreadyActive` | `*CallAlreadyActiveError` |
| `noActiveCall` | `*NoActiveCallError` |
| `callNotFound` | `*ValidationError` |
| `callNotCompleted` | `*ValidationError` |
| `callRejected` | `*CallRejectedError` (`.Question` 포함) |
| `internalError` | `*TelloServerError` (`auth` 응답이면 1011 종료 동반: 키를 확인하지 못함. 잠시 뒤 다시 접속) |

모든 오류는 게이트웨이 코드를 `.Code`에 담고 있습니다. **분기는 `.Code`로 하고
`.Message`로는 하지 마세요.** 메시지는 게이트웨이가 다시 쓸 수 있는 표시용
문자열입니다.

`createCall`은 통화가 만들어지기 전에 거부될 수도 있습니다. 이 경우
`call.created`도 `callId`도 과금도 없습니다. 게이트웨이는 재시도하지 않으므로
재시도 정책은 호출자 몫입니다.

| 게이트웨이 `code` | 오류 타입 | 대응 |
| --- | --- | --- |
| `insufficientCredit` | `*CallRefusedError` | 충전을 안내합니다. 재전송해도 소용없습니다 |
| `concurrentLimitExceeded` | `*CallRefusedError` | 자기 통화가 하나 끝나기를 기다렸다가 재시도합니다 |
| `callerNotVerified` | `*CallRefusedError` | 번호 인증을 안내합니다. 재전송해도 소용없습니다 |
| `noRepresentativeNumber` | `*CallRefusedError` | 발신 번호 설정을 안내합니다. 재전송해도 소용없습니다 |
| `callProviderUnauthorized` | `*CallProviderError` | 서비스 장애로 보고합니다. 재전송은 도움이 안 됩니다 |
| `callProviderDraining` | `*CallProviderError` | 나중에 재시도합니다 |
| `callProviderUnavailable` | `*CallProviderError` | 나중에 재시도합니다 |
| `callSetupFailed` | `*CallProviderError` | 실패로 보고합니다 |

명령 단위 오류는 소켓을 닫지 않고 `EventTypeError` 구독자에게 전달됩니다.
`WaitClosed`를 끝내는 오류는 이 통화의 `CreateCall`에 대한 응답(`requestId`가
일치하는 오류)뿐입니다. 그래서 실패한 `CreateCall`(예: `toRequired`,
`callRejected`)이 멈춘 채 남지 않습니다. `Answer`, `SendDtmf`, `GetSummary`,
`Cancel`이 실패해도 통화는 끝나지 않으므로, 그 오류는 `EventTypeError` 이벤트로만
전달되고 `WaitClosed`는 계속 기다립니다. 두 코드는 따로 다룹니다:

- `callAlreadyActive`는 통화를 연 `CreateCall`에 대한 응답일 때만 대기를
  끝냅니다. 게이트웨이는 직전 통화의 종료 이벤트를 보낸 뒤에도 잠시 그 통화를
  정리하므로, 이 통화는 시작되지 않은 것입니다. 잠시 뒤 다시 시도하세요. 진행
  중인 통화 도중 보낸 `CreateCall`에 대한 `callAlreadyActive`는 이벤트로만 오고,
  진행 중인 통화는 계속됩니다.
- `noActiveCall`은 대기를 끝내지 않습니다.

`EventTypeError` 핸들러는 프레임을 `tello.Event`로 받습니다. `.Question`·
`.RequiredScope`·`.IP`까지 채운 타입 있는 오류로 바꾸려면 `tello.ErrorForEvent`를
쓰세요:

```go
client.On(tello.EventTypeError, func(_ context.Context, event tello.Event) error {
	err := tello.ErrorForEvent(event)
	var invalid *tello.ValidationError
	if errors.As(err, &invalid) {
		log.Printf("command %s rejected: %s", event.RequestID, invalid.Code)
	}
	return nil
})
```

`WaitClosed`가 반환하는 오류:

- 인증 실패(`unauthenticated`·`ipNotAllowed` 프레임, 4401 종료, `auth.ok` 타임아웃) → `Connect`가 `*AuthenticationError` 반환
- 인증 때 키를 확인하지 못함(`internalError` 프레임 또는 1011 종료) → `Connect`가 `*TelloServerError` 반환. 거절이 아니므로 잠시 뒤 다시 접속
- 통화 시작 거부, 또는 `call.created` 이후 통화 스트림 실패(`CreateCall`에 대한 오류로 옴) → 위 표의 대응 오류
- 통화 도중 연결 끊김 → `*ConnectionClosedError`
- 다른 연결에 세션을 빼앗김(4429 종료) → `*SessionReplacedError`

이 오류 타입들은 공통 래퍼를 공유하지 않습니다. 그래서 `errors.As` 대상도
타입마다 따로 잡아야 합니다:

```go
var rejected *tello.CallRejectedError
if errors.As(err, &rejected) {
	log.Printf("call rejected: %s (%s)", rejected.Message, rejected.Question)
}
```

WS 수준 ping heartbeat는 게이트웨이가 주도하고, pong은 `gorilla/websocket`이
알아서 보냅니다. 재연결이나 세션 재개 프로토콜은 없습니다. 비정상 종료가 나면
재연결이 필요한 상황으로 보고 통화를 처음부터 다시 시작하세요.

## 7. REST 자원 API

`tello.API`는 서버용 API 키로 공개 REST API(`/v1`)를 호출합니다. WebSocket
`tello.Client`와 별도 타입이고, 여러 고루틴에서 함께 써도 되며, 요청을 자동으로
재시도하지 않습니다.

> **쓰기는 바로 운영에 반영됩니다.** 도구·에이전트·크루의 생성·수정·삭제는 저장과
> 배포, 운영 반영까지 한 번에 끝나고 다음 통화부터 쓰입니다. 따로 배포할 초안은
> 없습니다. **`CallDrafts.Confirm`은 실제 과금되는 전화를 겁니다.** 에이전트에
> 완성된 시나리오가 저장돼 있으면 `CallDrafts.Create`도 확인 없이 바로 겁니다
> (status `confirmed`).

```go
api, err := tello.NewAPI("") // TELLO_API_KEY
if err != nil {
	log.Fatal(err)
}
agents, err := api.Agents.List(ctx)
tool, err := api.Tools.Create(ctx, tello.Object{
	"type":         "endCall",
	"name":         "통화 종료",
	"functionName": "end_call",
}, tello.WithIdempotencyKey("tool-end-call-001"))
```

- **키**: `NewAPI` 인자, 없으면 `TELLO_API_KEY`(`NewClient`와 같은 규칙·같은 오류).
  `Authorization: Bearer <key>` 헤더로만 보내고, 오류나 출력 서식 어디에도 남기지
  않습니다.
- **base URL**: `WithAPIURL`, 없으면 `TELLO_API_URL`, 없으면
  `https://api.telloai.io/v1`. `https`만 받고, `http`는 `127.0.0.1`·`localhost`·`::1`
  에만 허용합니다. 끝의 `/`는 뗍니다.
- **타임아웃**: 조회(목록·상세)는 `WithAPITimeout`(기본 30초). 모든 쓰기 — 도구·에이전트·
  크루 생성·수정·삭제(저장·배포·운영 반영을 한 요청에서 함), `Crews.Generate`,
  `CallDrafts.Create`·`Revise`·`Confirm` — 는 `WithAPILongTimeout`(기본 120초)을
  씁니다. `ctx`로 더 일찍 끝낼 수 있습니다.
- **헤더**: `Accept: application/json`, 본문이 있으면 `Content-Type: application/json`,
  `User-Agent: tello-go-sdk/<tello.Version>`.
- **본문과 결과**는 `tello.Object`(`map[string]any`)입니다. 응답은 `UseNumber`로
  디코딩해 숫자는 `json.Number`로 오고, SDK가 아직 모르는 필드도 그대로 남습니다.

| 메서드 | HTTP | 성공 | 반환 |
| --- | --- | --- | --- |
| `api.Tools.List(ctx, tello.ToolListOptions{Limit, Cursor})` | `GET /tools?limit&cursor` | 200 | `{data, nextCursor}` (마지막 페이지면 `nextCursor`가 `nil`) |
| `api.Tools.Get(ctx, id)` | `GET /tools/{id}` | 200 | 도구 |
| `api.Tools.Create(ctx, body, opts...)` | `POST /tools` | 201 | `revision`이 든 도구 |
| `api.Tools.Update(ctx, id, patch)` | `PATCH /tools/{id}` | 200 | 도구 |
| `api.Tools.Delete(ctx, id)` | `DELETE /tools/{id}` | 204 | `error`만 |
| `api.Agents.List(ctx)` | `GET /agents` | 200 | `{data: [{id, name, description, isDefault}]}` |
| `api.Agents.Get` / `Create` / `Update` / `Delete` | `/agents[/{id}]` | 200 / 201 / 200 / 204 | 에이전트 / `error`만 |
| `api.Crews.List(ctx)` | `GET /crews` | 200 | `{data: [...]}` |
| `api.Crews.Get` / `Create` / `Update` / `Delete` | `/crews[/{id}]` | 200 / 201 / 200 / 204 | 크루 / `error`만 |
| `api.Crews.Generate(ctx, input, opts...)` | `POST /crews/generate` | 202 (같은 목표의 기존 작업이면 200) | 작업 `{id, status: "running", ...}` |
| `api.Crews.GenerateAndWait(ctx, input, tello.GenerateAndWaitOptions{})` | `Generate` 후 `GET /crew-generations/{id}` 폴링 | — | `status`가 `running`이 아니게 된 작업 |
| `api.CrewGenerations.Get(ctx, id)` | `GET /crew-generations/{id}` | 200 | 작업 `{id, status, crew, error, ...}` |
| `api.CallDrafts.Create(ctx, body, opts...)` | `POST /call-drafts` | 201 | 초안 |
| `api.CallDrafts.Revise(ctx, draftID, request, opts...)` | `POST /call-drafts/{id}/revise` 본문 `{"request": ...}` | 200 | **새 id**의 초안 |
| `api.CallDrafts.Confirm(ctx, draftID, opts...)` | `POST /call-drafts/{id}/confirm`, 본문 없음 | 201 | 통화 객체 |

경로 id는 URL 인코딩합니다. 빈 id는 요청 없이 오류를 돌려줍니다. 표와 다른 성공
상태나 JSON 객체가 아닌 본문은 code `invalid_response`인 `*APIError`입니다.

초안 status(`pendingConfirmation`·`needsClarification`·`refused`·`confirmed`)는
모두 201 성공이고 오류가 아닙니다. `draft["status"]`로 분기하세요.

`GenerateAndWait`는 `PollInterval`(기본 2초)마다 최대 `Timeout`(기본 600초)까지
폴링합니다. `failed` 작업은 오류가 아니라 작업 그대로 돌려줍니다. `Timeout`이 먼저
지나면 `JobID`가 든 `*tello.CrewGenerationTimeoutError`를 돌려주고
(`errors.Is(err, context.DeadlineExceeded)`가 참), 작업은 계속 진행됩니다.
`crew_generation_in_progress` 같은 `Generate`의 오류는 그대로 돌려줍니다.

### 멱등 키

`tello.WithIdempotencyKey(key)`는 도구·에이전트·크루 생성, `Crews.Generate`,
`CallDrafts.Create`·`Revise`·`Confirm`에 `Idempotency-Key`를 보냅니다. 주지 않으면
보내지 않습니다. 단 `CallDrafts.Confirm`은 키가 필수라 주지 않으면 UUID v4를 만들어
보냅니다. 키는 출력 가능한 ASCII(0x20–0x7E) 1~255자에 쉼표가 없어야 하고, 아니면 요청 없이 오류를
돌려줍니다. 모든 오류의 `IdempotencyKey`에 보낸 키가 실리므로 그 키로 다시 보낼 수
있습니다.

### 오류

| 오류 | 언제 | 필드 |
| --- | --- | --- |
| `*tello.APIError` | API가 오류로 답했거나, SDK가 받아들일 수 없는 응답이 옴 | `Status`, `Code`, `Message`, `Data`, `IdempotencyKey` |
| `*tello.APIConnectionError` | 완전한 응답이 없음: 연결 실패, 타임아웃, `ctx` 종료 | `Message`, `IdempotencyKey`, `Err`(원인) |
| `*tello.CrewGenerationTimeoutError` | `GenerateAndWait` 시간 초과 | `JobID` |

`Code`는 서버 `error` 코드 원문(snake_case)이라 공개 REST 오류 문서와 그대로
대조됩니다. 오류 봉투가 없는 응답(프록시 페이지 등)은 `http_<status>`와 HTTP 상태
문구가 됩니다. 분기는 `Code`와 `Status`로 하고 `Message`로는 하지 마세요.
`*APIConnectionError`로 실패한 쓰기는 **이미 반영됐을 수 있습니다**(확정은 이미 전화를
걸고 있을 수 있습니다). 다시 보내기 전에 조회·목록으로 확인하고, 같은 멱등 키로 다시
보내세요.

대표 코드(전체 목록은 공개 REST 오류 문서에 있습니다):

| HTTP | `Code` | 뜻 |
| --- | --- | --- |
| 401 | `unauthenticated` | 키가 없거나 잘못됐거나 폐기됨 |
| 403 | `insufficient_scope` | 키에 권한이 없음. `Data["requiredScope"]`에 필요한 권한 |
| 400 | `invalid_tool` / `invalid_agent` / `invalid_crew` | 필드 검증 실패. `Data["detail"]` 참고 |
| 404 | `tool_not_found` / `agent_not_found` / `crew_not_found` | 이 계정에 없음 |
| 404 | `crew_generation_not_found` / `draft_not_found` | 작업·초안이 없음 |
| 409 | `idempotency_key_reused` / `idempotency_in_progress` | 다른 본문에 쓴 키 / 같은 키가 처리 중 |
| 409 | `crew_generation_in_progress` | 다른 목표를 만드는 중. `Data["id"]`가 그 작업 |
| 409 | `draft_already_confirmed` | 이미 걸었음. `Data["callId"]`가 그 통화 |
| 422 | `tool_not_publishable` / `agent_not_publishable` / `crew_not_publishable` | 아무것도 저장하지 않음. `Data["reason"]` 참고 |
| 502 | `write_outcome_unknown` | 반영됐을 수 있음. 새 키로 다시 보내기 전에 확인 |
| 503 | `upstream_unavailable` | 반영되지 않음. 잠시 뒤 같은 요청을 다시 보냄 |

```go
call, err := api.CallDrafts.Confirm(ctx, draftID)
var apiErr *tello.APIError
var connErr *tello.APIConnectionError
switch {
case errors.As(err, &apiErr) && apiErr.Code == "draft_already_confirmed":
	log.Printf("이미 걸었음: call %v", apiErr.Data["callId"])
case errors.As(err, &apiErr):
	log.Printf("확정 거절: %d %s: %s", apiErr.Status, apiErr.Code, apiErr.Message)
case errors.As(err, &connErr):
	// 이미 걸었을 수 있습니다. 새 키가 아니라 같은 키로 다시 보냅니다.
	call, err = api.CallDrafts.Confirm(ctx, draftID, tello.WithIdempotencyKey(connErr.IdempotencyKey))
}
```

## 8. 예제

바로 실행해 볼 수 있는 프로그램이 [`examples/`](examples/README.ko.md)에
있습니다:

```bash
go run ./examples/basic-call      # 연결, 통화 1건, 각 턴에 응답
go run ./examples/agent-callback  # 전체 수명주기, 이력, cancel, 타입별 오류
go run ./examples/call-summary    # 게이트로 막아 둔 라이브 시나리오 + call.summary
```

세 예제 모두 실제 통화를 겁니다. 먼저
[`examples/README.ko.md`](examples/README.ko.md)를 읽으세요.

## 9. 버전 호환성

`tello-go 0.2.x`는 Tello WS 프로토콜 `1.0`을 구현합니다
(`tello.ProtocolVersion`).

`Connect`는 URL에 `?sdk=go&version=<tello.Version>&protocol=<tello.ProtocolVersion>`를
붙입니다. 사용자 경로와 다른 쿼리 쌍은 쓴 그대로(순서·인코딩·값 없는 플래그까지) 유지하고,
기존 `sdk`·`version`·`protocol` 쌍만 교체합니다. 게이트웨이가 어떤 클라이언트인지 로그로
남기는 용도입니다. 릴리스: `tello.Version`을 올려 커밋한 뒤 `vX.Y.Z` 태그를 겁니다. 태그와
`tello.Version`이 다르면 release-guard 워크플로가 실패합니다.

프레임 계약 전문은 [`docs/protocol/sdk-ws.v1.md`](docs/protocol/sdk-ws.v1.md)에
있고, [`docs/events/sdk-events.v1.schema.json`](docs/events/sdk-events.v1.schema.json)과
[`docs/errors/errors.v1.json`](docs/errors/errors.v1.json)이 함께 있습니다. 이
세 파일은 게이트웨이 구현 옆에 있는 정본에서 복사해 온 생성물입니다. 읽는 건
여기서, 고치는 건 정본에서 합니다.
