import { useState, type FormEvent } from "react"
import { Link, useLocation } from "react-router"
import { api, currentDevice } from "@/api/client"
import { loginByEmailCode, loginByPassword } from "@/api/endpoints"
import { errorText } from "@/api/errors"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { CodePurpose } from "@/gen/account/v1/account_pb"
import { useSession } from "@/stores/session"
import { looksLikeEmail } from "./email"
import { EmailCodeField, Field, FormError, PasswordInput, SubmitButton, TextInput } from "./fields"

type Method = "password" | "code"

export function LoginPage() {
  // 切换到注册页时保留 redirect 参数
  const { search } = useLocation()
  const signIn = useSession((s) => s.signIn)
  const [method, setMethod] = useState<Method>("password")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [code, setCode] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // 修改任一输入时清掉上次的错误提示
  const edit =
    <T,>(set: (value: T) => void) =>
    (value: T) => {
      set(value)
      setError(null)
    }

  const ready = looksLikeEmail(email) && (method === "password" ? password !== "" : code.length === 6)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready || submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const device = currentDevice()
      const res =
        method === "password"
          ? await api.send(loginByPassword, { email, password, device })
          : await api.send(loginByEmailCode, { email, code, device })
      // 成功后由路由守卫跳转，按钮保持加载状态直到离开页面
      signIn(res)
    } catch (err) {
      setError(errorText(err))
      setSubmitting(false)
    }
  }

  return (
    <div className="space-y-8">
      <div className="space-y-2">
        <h1 className="font-display text-3xl font-semibold tracking-tight">欢迎回来</h1>
        <p className="text-sm text-muted-foreground">登录 MarsChat，继续没聊完的话题</p>
      </div>

      <Tabs value={method} onValueChange={(value) => edit(setMethod)(value as Method)} className="gap-6">
        <TabsList className="h-10 w-full group-data-horizontal/tabs:h-10">
          <TabsTrigger value="password">密码登录</TabsTrigger>
          <TabsTrigger value="code">验证码登录</TabsTrigger>
        </TabsList>

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

          <TabsContent value="password">
            <Field label="密码" htmlFor="password">
              <PasswordInput
                id="password"
                autoComplete="current-password"
                placeholder="输入密码"
                maxLength={64}
                value={password}
                onChange={(e) => edit(setPassword)(e.target.value)}
              />
            </Field>
          </TabsContent>
          <TabsContent value="code">
            <EmailCodeField email={email} purpose={CodePurpose.LOGIN} value={code} onChange={edit(setCode)} />
          </TabsContent>

          <FormError message={error} />
          <SubmitButton loading={submitting} disabled={!ready}>
            登录
          </SubmitButton>
        </form>
      </Tabs>

      <p className="text-center text-sm text-muted-foreground">
        还没有账号？
        <Link to={{ pathname: "/register", search }} className="font-medium text-brand-text underline-offset-4 hover:underline">
          注册
        </Link>
      </p>
    </div>
  )
}
