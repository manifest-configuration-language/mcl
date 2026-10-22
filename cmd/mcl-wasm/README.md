# Browser WebAssembly API

`mcl.wasm`は`GOOS=js GOARCH=wasm go build ./cmd/mcl-wasm`で生成します。Web WorkerでGoの`wasm_exec.js`を読み込み、Goの実行開始後に`globalThis.mclWasmReady`を待ってから`globalThis.mclWasm.run(json)`を呼び出します。`run`はJSON文字列を受け取り、JSON文字列を同期的に返します。

## リクエスト

```json
{
  "requestId": "request-1",
  "revision": 12,
  "operation": "render",
  "entrypoint": "app/main.mcl",
  "source": "emit resource\n",
  "files": {
    "mcl.toml": { "encoding": "utf8", "content": "" },
    "schemas/widget.json": { "encoding": "utf8", "content": "{\"type\":\"object\"}" }
  }
}
```

`operation`には`parse`、`lint`、`format`、`evaluate`、`check`、`render`を指定します。`files`のキーは仮想プロジェクトルートからの相対パスです。MCL import、`mcl.toml`、ローカルManifest、Helm Chart、Kustomize入力を含められます。`entrypoint`には評価対象の相対パスを指定します。省略時は`main.mcl`を使います。選択したentrypointの内容には`source`を使い、同じパスを`files`にも指定できません。

## レスポンス

成功時は`ok: true`と操作結果を返します。`render`は`result.yaml`、`format`は`result.source`、`check`は`result.valid`とリソース数を返します。失敗時は`ok: false`と`error`を返し、入力診断にはCodeMirrorと同じUTF-16列の`line`と`column`を含めます。両方の応答に要求の`requestId`と`revision`をそのまま含めるため、Worker呼び出し元は古い結果を破棄できます。

入力ソースは64KiB、entrypointを含む仮想ファイルは4096個かつ1ファイル16MiB、entrypointを含む合計64MiB、整形またはレンダー結果は1MiBまでです。評価は4秒で中断します。

## ブラウザーのファイルシステム

GoのWASMランタイムはNode互換の`globalThis.fs`、`globalThis.path`、`globalThis.process`を参照します。ブラウザーWorkerはメモリ上の仮想FSをこれらのAPIとして公開し、FSコールバックを同期的に呼び出す必要があります。GoのWASM syscall層はFSコールバックの完了を同期的に待つため、非同期コールバックは評価を停止させます。

Browser RuntimeはWASMと`wasm_exec.js`を読み込んだ後に`fetch`を無効化するため、プロジェクト評価から外部ネットワークへ接続できません。ローカルの依存ファイルは`files`で渡してください。

## サイズ確認

`mise run wasm:check`はGo WASMをビルドし、raw、gzip、Brotliのサイズ、起動時間、レンダー時間とCLIの代表結果を確認します。2026-10-21時点のフルアダプター構成はraw 112,843,940 bytes、gzip 19,730,384 bytes、Brotli 12,231,514 bytesでした。サイズは計測値をログに出し、機能やビルドの合否条件には使いません。配信環境の転送量と対象端末の性能はデプロイ後に計測します。
