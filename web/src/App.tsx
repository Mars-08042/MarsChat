import { useEffect } from "react"
import { QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider } from "react-router"
import { router } from "@/app/router"
import { SessionEndedDialog } from "@/app/SessionEndedDialog"
import { Toaster } from "@/components/common/Toaster"
import { TooltipProvider } from "@/components/ui/tooltip"
import { PrototypePanel } from "@/dev/PrototypePanel"
import { queryClient } from "@/lib/query"
import { watchSystemTheme } from "@/lib/theme"

export function App() {
  useEffect(() => watchSystemTheme(), [])

  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider delay={400}>
        <RouterProvider router={router} />
        <SessionEndedDialog />
        <Toaster />
        {import.meta.env.DEV && <PrototypePanel />}
      </TooltipProvider>
    </QueryClientProvider>
  )
}
