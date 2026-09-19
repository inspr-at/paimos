// PAIMOS owns this narrow schema; the generated module bundles its Zod runtime.
import { string, boolean, minLength, maxLength } from "zod/mini";

export const nativeMessageShape = {
  to: string().check(maxLength(129)),
  body: string().check(minLength(1), maxLength(4096)),
  reply_to: string().check(maxLength(256)),
  is_action_request: boolean(),
  expects_reply: boolean(),
};
