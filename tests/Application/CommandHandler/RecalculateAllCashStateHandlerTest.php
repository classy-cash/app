<?php

declare(strict_types=1);

namespace App\Tests\Application\CommandHandler;

use App\Application\Command\RecalculateAllCashState;
use App\Application\Command\RecalculateCashState;
use App\Application\CommandHandler\RecalculateAllCashStateHandler;
use PHPUnit\Framework\Attributes\Group;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Messenger\Envelope;
use Symfony\Component\Messenger\MessageBusInterface;

#[Group('unit')]
final class RecalculateAllCashStateHandlerTest extends TestCase
{
    public function testDispatchesUnscopedRecalculateCashState(): void
    {
        $commandBus = $this->createMock(MessageBusInterface::class);
        $commandBus->expects($this->once())
            ->method('dispatch')
            ->with($this->callback(static fn(object $message): bool => $message instanceof RecalculateCashState
                && $message->payment === null
                && $message->transfer === null
                && $message->expense === null
                && $message->fromDate === null))
            ->willReturnCallback(static fn(object $message): Envelope => new Envelope($message));

        new RecalculateAllCashStateHandler($commandBus)(new RecalculateAllCashState());
    }
}
