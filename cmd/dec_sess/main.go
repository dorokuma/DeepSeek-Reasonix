package main
import (
  "fmt"
  "os"
  "reasonix/internal/sessioncrypt"
)
func main() {
  if len(os.Args) < 2 { fmt.Fprintln(os.Stderr,"usage: dec_sess <file>"); os.Exit(2) }
  data, err := os.ReadFile(os.Args[1])
  if err != nil { panic(err) }
  if sessioncrypt.IsEncrypted(data) {
    plain, err := sessioncrypt.Decrypt(data)
    if err != nil { panic(err) }
    data = plain
  }
  os.Stdout.Write(data)
}
