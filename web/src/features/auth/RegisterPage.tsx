import { useState, type FormEvent } from "react"
import { motion } from "motion/react"
import { Link, useLocation } from "react-router"
import { Circle, CircleCheck, CircleX } from "lucide-react"
import { api, currentDevice } from "@/api/client"
import { register } from "@/api/endpoints"
import { errorText } from "@/api/errors"
import { CodePurpose } from "@/gen/account/v1/account_pb"
import { springs } from "@/lib/motion"
import { cn } from "@/lib/utils"
import { useSession } from "@/stores/session"
import { looksLikeEmail } from "./email"
import { EmailCodeField, Field, FormError, PasswordInput, SubmitButton, TextInput } from "./fields"
import { isValidPassword, PASSWORD_RULES, type PasswordRule } from "./password"

// 与 RegisterRequest.nickname 的 max_len 一致（按码点计数）
const NICKNAME_MAX = 16

export function RegisterPage() {
  // 切换到登录页时保留 redirect 参数
  const { search } = useLocation()
  const signIn = useSession((s) => s.signIn)
  const [email, setEmail] = useState("")
  const [code, setCode] = useState("")
  const [nickname, setNickname] = useState("")
  const [password, setPassword] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // 修改任一输入时清掉上次的错误提示
  const edit =
    <T,>(set: (value: T) => void) =>
    (value: T) => {
      set(value)
      setError(null)
    }

  const nicknameLength = [...nickname.trim()].length
  const ready =
    looksLikeEmail(email) &&
    code.length === 6 &&
    nicknameLength > 0 &&
    nicknameLength <= NICKNAME_MAX &&
    isValidPassword(password)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready || submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const res = await api.send(register, { email, code, password, nickname: nickname.trim(), device: currentDevice() })
      // 注册即登录，成功后由路由守卫跳转
      signIn(res)
    } catch (err) {
      setError(errorText(err))
      setSubmitting(false)
    }
  }

  return (
    <div className="space-y-8">
      <div className="space-y-2">
        <h1 className="font-display text-3xl font-semibold tracking-tight">创建账号</h1>
        <p className="text-sm text-muted-foreground">用邮箱注册，几秒钟就能开始聊天</p>
      </div>

      <form onSubmit={submit} className="space-y-5" noValidate>
        <Field label="邮箱" htmlFor="email">
          <TextInput
            id="email"
            type="email"
            autoComplete="email"
            placeholder="you@example.com"
            value={email}
            onChange={(e) => edit(setEmail)(e.target.value.trim())}
          />
        </Field>

        <EmailCodeField email={email} purpose={CodePurpose.REGISTER} value={code} onChange={edit(setCode)} />

        <Field
          label="昵称"
          htmlFor="nickname"
          aside={
            <span className={cn("font-mono text-xs", nicknameLength > NICKNAME_MAX ? "text-destructive" : "text-muted-foreground")}>
              {nicknameLength}/{NICKNAME_MAX}
            </span>
          }
        >
          <TextInput
            id="nickname"
            autoComplete="nickname"
            placeholder="大家怎么称呼你"
            value={nickname}
            onChange={(e) => edit(setNickname)(e.target.value)}
            aria-invalid={nicknameLength > NICKNAME_MAX || undefined}
          />
        </Field>

        <Field label="密码" htmlFor="password">
          <PasswordInput
            id="password"
            autoComplete="new-password"
            placeholder="设置登录密码"
            value={password}
            onChange={(e) => edit(setPassword)(e.target.value)}
            aria-describedby="password-rules"
          />
          <ul id="password-rules" className="grid grid-cols-2 gap-x-3 gap-y-1.5 pt-1 text-xs">
            {PASSWORD_RULES.map((rule) => (
              <RuleItem key={rule.id} rule={rule} password={password} />
            ))}
          </ul>
        </Field>

        <FormError message={error} />
        <SubmitButton loading={submitting} disabled={!ready}>
          注册并登录
        </SubmitButton>
      </form>

      <p className="text-center text-sm text-muted-foreground">
        已有账号？
        <Link to={{ pathname: "/login", search }} className="font-medium text-brand-text underline-offset-4 hover:underline">
          登录
        </Link>
      </p>
    </div>
  )
}

// 未输入时全部显示为待满足；字符集和长度上限属于「违反」，用错误样式提示，其余规则只区分满足与未满足
function RuleItem({ rule, password }: { rule: PasswordRule; password: string }) {
  const ok = password !== "" && rule.test(password)
  const violated = password !== "" && !ok && (rule.id === "charset" || (rule.id === "length" && password.length > 64))
  const state = ok ? "ok" : violated ? "error" : "idle"
  const Icon = state === "ok" ? CircleCheck : state === "error" ? CircleX : Circle

  return (
    <li
      className={cn(
        "flex items-center gap-1.5 transition-colors",
        state === "ok" && "text-success",
        state === "error" && "text-destructive",
        state === "idle" && "text-muted-foreground",
      )}
    >
      <motion.span key={state} initial={{ scale: 0.4 }} animate={{ scale: 1 }} transition={springs.snappy} className="flex">
        <Icon className="size-3.5" />
      </motion.span>
      {rule.label}
    </li>
  )
}
