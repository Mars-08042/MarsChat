import { useState, type ComponentProps, type ReactNode } from "react"
import { REGEXP_ONLY_DIGITS } from "input-otp"
import { ArrowRight, CircleAlert, Eye, EyeOff, LoaderCircle } from "lucide-react"
import { api } from "@/api/client"
import { sendEmailCode } from "@/api/endpoints"
import { errorText, isApiError } from "@/api/errors"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { InputOTP, InputOTPGroup, InputOTPSlot } from "@/components/ui/input-otp"
import { Label } from "@/components/ui/label"
import { useCountdown } from "@/lib/use-countdown"
import type { CodePurpose } from "@/gen/account/v1/account_pb"
import { cn } from "@/lib/utils"

import { looksLikeEmail } from "./email"

// 与后端 account 服务配置一致：同一邮箱两次发送至少间隔 60 秒（以响应的 cooldown_seconds 为准），验证码 5 分钟内有效
const RESEND_SECONDS = 60
const CODE_TTL_MINUTES = 5
// 限流的剩余时间不超过这个值时在按钮上倒计时；更长（例如每日上限要等到第二天）只显示错误提示
const MAX_COUNTDOWN_SECONDS = 600

export function Field({ label, htmlFor, aside, children }: { label: string; htmlFor: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <Label htmlFor={htmlFor}>{label}</Label>
        {aside}
      </div>
      {children}
    </div>
  )
}

export function TextInput({ className, ...props }: ComponentProps<typeof Input>) {
  return <Input className={cn("h-11 rounded-lg px-3.5 text-[15px] md:text-[15px]", className)} {...props} />
}

export function PasswordInput(props: Omit<ComponentProps<typeof Input>, "type">) {
  const [visible, setVisible] = useState(false)
  return (
    <div className="relative">
      <TextInput type={visible ? "text" : "password"} className="pr-11" {...props} />
      <button
        type="button"
        onClick={() => setVisible((v) => !v)}
        aria-label={visible ? "隐藏密码" : "显示密码"}
        className="absolute inset-y-0 right-0 flex w-11 items-center justify-center rounded-r-lg text-muted-foreground transition-colors outline-none hover:text-foreground focus-visible:text-foreground"
      >
        {visible ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  )
}

interface EmailCodeFieldProps {
  email: string
  purpose: CodePurpose
  value: string
  onChange: (value: string) => void
}

// 6 位分格验证码，支持粘贴；「发送验证码」按钮按服务端返回的间隔倒计时
export function EmailCodeField({ email, purpose, value, onChange }: EmailCodeFieldProps) {
  const { secondsLeft, start } = useCountdown()
  const [sending, setSending] = useState(false)
  const [sentTo, setSentTo] = useState<string | null>(null)
  // 发送失败的提示只对当时的邮箱有效，改了邮箱就不再显示
  const [error, setError] = useState<{ email: string; text: string } | null>(null)
  const sendError = error?.email === email ? error.text : null

  const send = async () => {
    setSending(true)
    setError(null)
    try {
      const res = await api.send(sendEmailCode, { email, purpose })
      setSentTo(email)
      start(res.cooldownSeconds > 0 ? res.cooldownSeconds : RESEND_SECONDS)
    } catch (e) {
      setError({ email, text: errorText(e) })
      const retry = isApiError(e) ? e.retryAfterSeconds : undefined
      if (retry && retry <= MAX_COUNTDOWN_SECONDS) start(retry)
    } finally {
      setSending(false)
    }
  }

  const canSend = looksLikeEmail(email) && secondsLeft === 0 && !sending

  return (
    <Field
      label="验证码"
      htmlFor="email-code"
      aside={
        <button
          type="button"
          onClick={send}
          disabled={!canSend}
          className="text-xs font-medium text-brand-text transition-colors outline-none hover:underline focus-visible:underline disabled:pointer-events-none disabled:text-muted-foreground"
        >
          {sending ? "发送中…" : secondsLeft > 0 ? `${secondsLeft} 秒后可重新发送` : sentTo ? "重新发送" : "发送验证码"}
        </button>
      }
    >
      <InputOTP id="email-code" maxLength={6} value={value} onChange={onChange} pattern={REGEXP_ONLY_DIGITS} autoComplete="one-time-code">
        <InputOTPGroup className="w-full gap-2">
          {Array.from({ length: 6 }, (_, i) => (
            <InputOTPSlot
              key={i}
              index={i}
              className="h-12 flex-1 rounded-lg border font-mono text-lg first:rounded-lg last:rounded-lg"
            />
          ))}
        </InputOTPGroup>
      </InputOTP>
      {sendError ? (
        <p role="alert" className="text-xs text-destructive">
          {sendError}
        </p>
      ) : (
        sentTo && (
          <p className="text-xs text-muted-foreground">
            验证码已发送到 <span className="font-mono text-foreground/80">{sentTo}</span>，{CODE_TTL_MINUTES} 分钟内有效
          </p>
        )
      )}
    </Field>
  )
}

// 提交失败的提示，放在提交按钮上方
export function FormError({ message }: { message: string | null }) {
  if (!message) return null
  return (
    <p
      role="alert"
      className="flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2.5 text-sm text-destructive"
    >
      <CircleAlert className="mt-0.5 size-4 shrink-0" />
      {message}
    </p>
  )
}

// 主按钮：深火星红底白字（白字放在 #E2452C 上对比度不够），悬停时一道光扫过
export function SubmitButton({ loading, disabled, children }: { loading: boolean; disabled?: boolean; children: ReactNode }) {
  return (
    <Button
      type="submit"
      disabled={disabled || loading}
      className={cn(
        "group/submit relative h-11 w-full overflow-hidden rounded-lg text-[15px]",
        "before:absolute before:inset-y-0 before:left-0 before:w-1/3 before:-translate-x-full before:skew-x-[-20deg] before:bg-white/20 before:transition-transform before:duration-700",
        "hover:before:translate-x-[400%] motion-reduce:before:hidden",
      )}
    >
      {loading ? (
        <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" />
      ) : (
        <>
          {children}
          <ArrowRight className="size-4 transition-transform group-hover/submit:translate-x-0.5" />
        </>
      )}
    </Button>
  )
}
