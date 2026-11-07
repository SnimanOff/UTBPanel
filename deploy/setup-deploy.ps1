# Sets up the automatic deploy (deploy.yml) from a Windows machine that can
# already reach the server over SSH and has the gh CLI signed in:
#
#   powershell -ExecutionPolicy Bypass -File deploy\setup-deploy.ps1 -Server root@snimanoff.space
#
# What it does:
#   1. creates a separate SSH key for GitHub Actions (~\.ssh\utbpanel-deploy);
#   2. adds its public half to the server (your usual SSH login is used, the
#      server may ask for your password once);
#   3. finds the panel's directory on the server (docker compose ls);
#   4. stores DEPLOY_* secrets and variables in the GitHub repository.
# The private key goes straight from the file to GitHub and is not printed.
param(
    [Parameter(Mandatory = $true)][string]$Server,  # user@host
    [int]$Port = 22,
    [string]$Repo = 'SnimanOff/UTBPanel',
    [string]$Path = ''                              # panel directory, found when empty
)
# Native tools (ssh, ssh-keyscan) write notes to stderr; with 'Stop' Windows
# PowerShell 5.1 would treat them as errors. Exit codes are checked instead.
$ErrorActionPreference = 'Continue'

function Step($text) { Write-Host "`n== $text" -ForegroundColor Cyan }
function Fail($text) { Write-Host "ОШИБКА: $text" -ForegroundColor Red; exit 1 }

if ($Server -notmatch '^([^@]+)@(.+)$') { Fail "укажите сервер как пользователь@адрес, например root@snimanoff.space" }
$user = $Matches[1]
$hostName = $Matches[2]

foreach ($tool in 'ssh', 'ssh-keygen', 'ssh-keyscan', 'gh') {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { Fail "не найдена программа $tool" }
}
gh auth status *> $null
if ($LASTEXITCODE -ne 0) { Fail "gh не авторизован: выполните gh auth login" }

$key = Join-Path $HOME '.ssh\utbpanel-deploy'
Step "1/4 Ключ для GitHub Actions"
if (Test-Path $key) {
    Write-Host "уже есть: $key"
} else {
    New-Item -ItemType Directory -Force (Split-Path $key) | Out-Null
    ssh-keygen -q -t ed25519 -N '""' -C 'utbpanel-deploy' -f $key
    if ($LASTEXITCODE -ne 0) { Fail "ssh-keygen не создал ключ" }
    Write-Host "создан: $key"
}
$pub = (Get-Content "$key.pub" -Raw).Trim()

Step "2/4 Ключ на сервер $Server (может спросить пароль)"
$install = "mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && (grep -qxF '$pub' ~/.ssh/authorized_keys || echo '$pub' >> ~/.ssh/authorized_keys)"
ssh -p $Port $Server $install
if ($LASTEXITCODE -ne 0) { Fail "не удалось зайти на $Server" }
# The new key must work on its own and be allowed to run docker.
$check = ssh -i $key -p $Port -o BatchMode=yes -o IdentitiesOnly=yes $Server 'docker compose version >/dev/null && echo ok'
if ($LASTEXITCODE -ne 0 -or $check -ne 'ok') { Fail "по новому ключу не получается запустить docker compose: пользователь $user должен быть root или в группе docker" }
Write-Host "ключ работает, docker доступен"

Step "3/4 Папка панели на сервере"
if (-not $Path) {
    $json = ssh -i $key -p $Port -o BatchMode=yes -o IdentitiesOnly=yes $Server 'docker compose ls -a --format json'
    $projects = @($json | ConvertFrom-Json)
    $found = @()
    foreach ($p in $projects) {
        foreach ($f in ($p.ConfigFiles -split ',')) {
            $dir = $f.Trim() -replace '/[^/]+$', ''
            # No double quotes in remote commands: PowerShell 5.1 mangles them.
            $has = ssh -i $key -p $Port -o BatchMode=yes -o IdentitiesOnly=yes $Server "grep -qs utbpanel '$($f.Trim())' && test -f '$dir/.env' && echo yes"
            if ($has -eq 'yes') { $found += $dir }
        }
    }
    $found = @($found | Select-Object -Unique)
    if ($found.Count -eq 1) {
        $Path = $found[0]
    } elseif ($found.Count -gt 1) {
        Fail "нашлось несколько установок: $($found -join ', '). Запустите ещё раз с -Path <папка>"
    } else {
        Fail "не нашла папку с docker-compose.yml панели. Запустите ещё раз с -Path <папка>"
    }
}
Write-Host "папка: $Path"

Step "4/4 Секреты и переменные в GitHub ($Repo)"
function Saved($what) { if ($LASTEXITCODE -ne 0) { Fail "gh не сохранил $what" } else { Write-Host "сохранено: $what" } }
Get-Content $key -Raw | gh secret set DEPLOY_SSH_KEY --repo $Repo; Saved 'DEPLOY_SSH_KEY'
$known = @(ssh-keyscan -p $Port $hostName 2>$null | Where-Object { $_ -and -not $_.StartsWith('#') })
if ($known.Count -eq 0) { Fail "ssh-keyscan не получил ключ сервера $hostName" }
($known -join "`n") | gh secret set DEPLOY_KNOWN_HOSTS --repo $Repo; Saved 'DEPLOY_KNOWN_HOSTS'
gh secret set DEPLOY_HOST --repo $Repo --body $hostName; Saved 'DEPLOY_HOST'
gh secret set DEPLOY_USER --repo $Repo --body $user; Saved 'DEPLOY_USER'
gh variable set DEPLOY_PATH --repo $Repo --body $Path; Saved 'DEPLOY_PATH'
if ($Port -ne 22) { gh variable set DEPLOY_PORT --repo $Repo --body "$Port"; Saved 'DEPLOY_PORT' }

Write-Host "`nГотово: автодеплой настроен. Новый тег vX.Y.Z соберёт образ и обновит сервер." -ForegroundColor Green
