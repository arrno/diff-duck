import { Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogTitle, AlertDialogTrigger,
} from "@/components/ui/dialog";

/** Deleting cannot be undone, so it asks first. */
export function DeleteButton({ label = "Delete", onDelete }: { label?: string; onDelete: () => Promise<void> | void }) {
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button type="button" variant="outline" size="sm"><Trash2 aria-hidden />{label}</Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogTitle>{label === "Delete" ? "Delete this diff?" : "Stop and delete this diff?"}</AlertDialogTitle>
        <AlertDialogDescription>This can't be undone.</AlertDialogDescription>
        <div className="flex justify-end gap-2">
          <AlertDialogCancel asChild>
            <Button type="button" variant="outline" size="sm">Keep</Button>
          </AlertDialogCancel>
          <AlertDialogAction asChild>
            <Button type="button" size="sm" onClick={() => void onDelete()}>Yes, {label.toLowerCase()}</Button>
          </AlertDialogAction>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  );
}
