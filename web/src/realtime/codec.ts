// WebSocket 帧编解码（data-flow 3.1）：子协议 marschat.v1.proto，每条二进制消息恰好一帧 protobuf。

import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf"
import {
  ClientFrameSchema,
  ServerFrameSchema,
  type ClientFrame,
  type ServerFrame,
} from "@/gen/access/v1/frame_pb"

export const SUBPROTOCOL = "marschat.v1.proto"

export type ClientFrameInit = MessageInitShape<typeof ClientFrameSchema>
export type ClientBody = ClientFrameInit["body"]

export function encodeClientFrame(frame: ClientFrameInit): Uint8Array<ArrayBuffer> {
  return toBinary(ClientFrameSchema, create(ClientFrameSchema, frame)) as Uint8Array<ArrayBuffer>
}

export function decodeClientFrame(data: Uint8Array): ClientFrame {
  return fromBinary(ClientFrameSchema, data)
}

export function encodeServerFrame(frame: MessageInitShape<typeof ServerFrameSchema>): Uint8Array<ArrayBuffer> {
  return toBinary(ServerFrameSchema, create(ServerFrameSchema, frame)) as Uint8Array<ArrayBuffer>
}

export function decodeServerFrame(data: ArrayBuffer | Uint8Array): ServerFrame {
  return fromBinary(ServerFrameSchema, data instanceof Uint8Array ? data : new Uint8Array(data))
}
