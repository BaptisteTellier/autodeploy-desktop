#Requires -Version 7.0
<#
.SYNOPSIS
    Generates build/icon.ico — the autodeploy-desktop application icon.
.DESCRIPTION
    Draws an emerald rounded-square with a white rocket glyph (matching the
    in-app branding) at several sizes and packs them into a single multi-size
    .ico (PNG-compressed entries, supported on Vista+). The icon is embedded
    into the exe via a .syso resource (see build-bundle.ps1 / make-syso step).
    Re-run after changing the branding. Requires Windows (System.Drawing).
#>
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

$RepoRoot = $PSScriptRoot | Split-Path -Parent
$OutIco   = Join-Path $RepoRoot 'build\icon.ico'
New-Item -ItemType Directory -Force -Path (Split-Path $OutIco -Parent) | Out-Null

$emerald = [System.Drawing.Color]::FromArgb(4, 120, 87)   # tailwind emerald-700
$rocket  = [System.Char]::ConvertFromUtf32(0x1F680)        # 🚀

function New-IconBitmap([int]$size) {
    $bmp = New-Object System.Drawing.Bitmap($size, $size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode     = 'AntiAlias'
    $g.TextRenderingHint = 'AntiAliasGridFit'
    $g.Clear([System.Drawing.Color]::Transparent)

    # Rounded-square emerald background.
    $r = [Math]::Max(2, [int]($size * 0.18))
    $d = $r * 2
    $m = [Math]::Max(0, [int]($size * 0.04))   # small margin
    $w = $size - 2 * $m
    $path = New-Object System.Drawing.Drawing2D.GraphicsPath
    $path.AddArc($m, $m, $d, $d, 180, 90)
    $path.AddArc($m + $w - $d, $m, $d, $d, 270, 90)
    $path.AddArc($m + $w - $d, $m + $w - $d, $d, $d, 0, 90)
    $path.AddArc($m, $m + $w - $d, $d, $d, 90, 90)
    $path.CloseFigure()
    $brush = New-Object System.Drawing.SolidBrush($emerald)
    $g.FillPath($brush, $path)

    # White rocket glyph centered.
    $font = New-Object System.Drawing.Font('Segoe UI Emoji', [single]($size * 0.52), [System.Drawing.FontStyle]::Regular, [System.Drawing.GraphicsUnit]::Pixel)
    $fmt = New-Object System.Drawing.StringFormat
    $fmt.Alignment = 'Center'; $fmt.LineAlignment = 'Center'
    $white = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::White)
    $rect = New-Object System.Drawing.RectangleF(0, 0, $size, $size)
    $g.DrawString($rocket, $font, $white, $rect, $fmt)

    $g.Dispose(); $brush.Dispose(); $white.Dispose(); $font.Dispose(); $path.Dispose()
    return $bmp
}

# Build PNG bytes for each size.
$sizes = @(16, 24, 32, 48, 64, 128, 256)
$pngs = foreach ($s in $sizes) {
    $bmp = New-IconBitmap $s
    $ms = New-Object System.IO.MemoryStream
    $bmp.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
    , $ms.ToArray()
}

# Assemble the .ico (ICONDIR + ICONDIRENTRY[] + PNG data).
$out = New-Object System.IO.MemoryStream
$bw = New-Object System.IO.BinaryWriter($out)
$count = $sizes.Count
$bw.Write([uint16]0); $bw.Write([uint16]1); $bw.Write([uint16]$count)   # reserved, type=1(icon), count
$offset = 6 + 16 * $count
for ($i = 0; $i -lt $count; $i++) {
    $s = $sizes[$i]; $len = $pngs[$i].Length
    $bw.Write([byte]([Math]::Min($s, 255) % 256))   # width  (0 => 256)
    $bw.Write([byte]([Math]::Min($s, 255) % 256))   # height (0 => 256)
    $bw.Write([byte]0)            # color count
    $bw.Write([byte]0)            # reserved
    $bw.Write([uint16]1)          # planes
    $bw.Write([uint16]32)         # bpp
    $bw.Write([uint32]$len)       # bytes of PNG
    $bw.Write([uint32]$offset)    # offset
    $offset += $len
}
foreach ($p in $pngs) { $bw.Write($p) }
$bw.Flush()
[System.IO.File]::WriteAllBytes($OutIco, $out.ToArray())
$bw.Dispose(); $out.Dispose()

Write-Host "Wrote $OutIco ($((Get-Item $OutIco).Length) bytes, sizes: $($sizes -join ','))"
