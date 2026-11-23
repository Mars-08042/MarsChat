import { useState, type ReactNode } from "react"
import { LoaderCircle } from "lucide-react"
import { toast } from "sonner"
import { errorText } from "@/api/errors"
import { AvatarUpload } from "@/components/common/AvatarUpload"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import type { User } from "@/gen/account/v1/account_pb"
import { cn, codePoints } from "@/lib/utils"
import { useSession } from "@/stores/session"
import { Choice } from "./Choice"
import { BIRTHDAY_MIN, GENDER_OPTIONS, genderOf, NICKNAME_MAX, SIGNATURE_MAX, updateProfile, type GenderChoice, type ProfilePatch } from "./queries"

// 本地日期的 YYYY-MM-DD，作为生日的上限
function today(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`
}

export function ProfileTab() {
  const user = useSession((s) => s.user)
  if (!user) {
    return (
      <div className="flex justify-center py-12 text-muted-foreground">
        <LoaderCircle aria-label="加载中" className="animate-spin motion-reduce:animate-none" />
      </div>
    )
  }
  // 保存成功后登录状态里的资料更新，按新资料重新挂载表单
  return <ProfileForm key={`${user.nickname}|${user.gender}|${user.birthday}|${user.signature}`} user={user} />
}

function ProfileForm({ user }: { user: User }) {
  const [nickname, setNickname] = useState(user.nickname)
  const [gender, setGender] = useState<GenderChoice>(genderOf(user.gender))
  const [birthday, setBirthday] = useState(user.birthday)
  const [signature, setSignature] = useState(user.signature)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string>()

  const nicknameLength = codePoints(nickname.trim())
  const signatureLength = codePoints(signature.trim())
  const max = today()
  const birthdayValid = birthday === "" || (birthday >= BIRTHDAY_MIN && birthday <= max)
  const valid = nicknameLength >= 1 && nicknameLength <= NICKNAME_MAX && signatureLength <= SIGNATURE_MAX && birthdayValid

  // 只发送变化的字段
  const patch: ProfilePatch = {
    nickname: nickname.trim() !== user.nickname ? nickname.trim() : undefined,
    gender: gender !== genderOf(user.gender) ? gender : undefined,
    birthday: birthday !== user.birthday ? birthday : undefined,
    signature: signature.trim() !== user.signature ? signature.trim() : undefined,
  }
  const dirty = Object.values(patch).some((v) => v !== undefined)

  const reset = () => {
    setNickname(user.nickname)
    setGender(genderOf(user.gender))
    setBirthday(user.birthday)
    setSignature(user.signature)
    setError(undefined)
  }

  const save = async () => {
    setSaving(true)
    setError(undefined)
    try {
      await updateProfile(patch)
      toast("资料已保存")
    } catch (e) {
      // 客户端已经校验过昵称和签名，VALIDATOR 基本只会是生日（服务端按北京时间判断「今天」）
      setError(errorText(e, patch.birthday !== undefined ? { VALIDATOR: "生日需要在 1900 年 1 月 1 日之后，且不晚于今天" } : undefined))
      setSaving(false)
    }
  }

  return (
    <form
      className="flex flex-col gap-5"
      onSubmit={(e) => {
        e.preventDefault()
        if (valid && dirty && !saving) void save()
      }}
    >
      <AvatarUpload
        seed={user.id.toString()}
        name={nickname.trim() || user.nickname}
        avatarId={user.avatar || undefined}
        onChange={async (avatar) => void (await updateProfile({ avatar }))}
      />

      <Field label="昵称" htmlFor="profile-nickname" count={`${nicknameLength}/${NICKNAME_MAX}`} over={nicknameLength > NICKNAME_MAX}>
        <Input id="profile-nickname" value={nickname} onChange={(e) => setNickname(e.target.value)} autoComplete="nickname" aria-invalid={nicknameLength === 0 || nicknameLength > NICKNAME_MAX} />
        {nicknameLength === 0 && <p className="text-xs text-destructive">昵称不能为空</p>}
      </Field>

      <Field label="性别">
        <Choice label="性别" value={gender} options={GENDER_OPTIONS} onPick={setGender} className="max-w-sm" />
      </Field>

      <Field label="生日" htmlFor="profile-birthday" hint="只有你自己能看到">
        <div className="flex max-w-sm gap-2">
          <Input
            id="profile-birthday"
            type="date"
            min={BIRTHDAY_MIN}
            max={max}
            value={birthday}
            onChange={(e) => setBirthday(e.target.value)}
            aria-invalid={!birthdayValid}
          />
          {birthday && (
            <Button type="button" variant="ghost" onClick={() => setBirthday("")}>
              清除
            </Button>
          )}
        </div>
        {!birthdayValid && <p className="text-xs text-destructive">生日需要在 1900 年 1 月 1 日之后，且不晚于今天</p>}
      </Field>

      <Field label="个性签名" htmlFor="profile-signature" count={`${signatureLength}/${SIGNATURE_MAX}`} over={signatureLength > SIGNATURE_MAX}>
        <Textarea id="profile-signature" rows={3} value={signature} onChange={(e) => setSignature(e.target.value)} placeholder="一句话介绍自己" />
      </Field>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex justify-end gap-2 border-t pt-4">
        <Button type="button" variant="ghost" disabled={!dirty || saving} onClick={reset}>
          撤销修改
        </Button>
        <Button type="submit" disabled={!dirty || !valid || saving}>
          {saving && <LoaderCircle className="animate-spin motion-reduce:animate-none" />}
          {saving ? "保存中…" : "保存"}
        </Button>
      </div>
    </form>
  )
}

function Field({
  label,
  htmlFor,
  hint,
  count,
  over,
  children,
}: {
  label: string
  htmlFor?: string
  hint?: string
  count?: string
  over?: boolean
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-baseline justify-between gap-2">
        {htmlFor ? (
          <label htmlFor={htmlFor} className="text-sm font-medium">
            {label}
          </label>
        ) : (
          <span className="text-sm font-medium">{label}</span>
        )}
        {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
        {count && <span className={cn("font-mono text-xs", over ? "text-destructive" : "text-muted-foreground")}>{count}</span>}
      </div>
      {children}
    </div>
  )
}
