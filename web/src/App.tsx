import { useEffect } from "react"
import { RouterProvider } from "react-router"
import { router } from "@/app/router"
import { SessionEndedDialog } from "@/app/SessionEndedDialog"
import { TooltipProvider } from "@/components/ui/tooltip"
import { PrototypePanel } from "@/dev/PrototypePanel"
import { watchSystemTheme } from "@/lib/theme"

export function App() {
  useEffect(() => watchSystemTheme(), [])

  return (
    <TooltipProvider delay={400}>
      <RouterProvider router={router} />
      <SessionEndedDialog />
      {import.meta.env.DEV && <PrototypePanel />}
    </TooltipProvider>
  )
}
