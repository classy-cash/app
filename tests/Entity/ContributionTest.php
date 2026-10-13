<?php

declare(strict_types=1);

namespace App\Tests\Entity;

use App\Entity\ClassCouncil\ClassRoom;
use App\Entity\ClassCouncil\Student;
use App\Entity\Contribution;
use Brick\Money\Money;
use PHPUnit\Framework\Attributes\Group;
use PHPUnit\Framework\TestCase;

#[Group('unit')]
final class ContributionTest extends TestCase
{
    public function testRemainingAmountIsExpectedAmountForAllStudentsMinusTotalPaid(): void
    {
        $contribution = $this->contribution(Money::of('25.50', 'PLN'), students: 3);
        $contribution->setTotalPaid(Money::of('30.00', 'PLN'));

        // 3 students x 25.50 = 76.50, 30.00 already paid
        self::assertTrue($contribution->getRemainingAmount()->isEqualTo(Money::of('46.50', 'PLN')));
    }

    public function testRemainingAmountIsZeroWhenEveryoneHasPaid(): void
    {
        $contribution = $this->contribution(Money::of(40, 'PLN'), students: 2);
        $contribution->setTotalPaid(Money::of(80, 'PLN'));

        self::assertTrue($contribution->getRemainingAmount()->isZero());
    }

    public function testRemainingAmountIsNegativeWhenOverpaid(): void
    {
        $contribution = $this->contribution(Money::of(10, 'PLN'), students: 2);
        $contribution->setTotalPaid(Money::of(25, 'PLN'));

        self::assertTrue($contribution->getRemainingAmount()->isEqualTo(Money::of(-5, 'PLN')));
    }

    public function testRemainingAmountWithoutStudentsIsMinusTotalPaid(): void
    {
        $contribution = $this->contribution(Money::of(10, 'PLN'), students: 0);
        $contribution->setTotalPaid(Money::of(15, 'PLN'));

        self::assertTrue($contribution->getRemainingAmount()->isEqualTo(Money::of(-15, 'PLN')));
    }

    public function testRemainingAmountFollowsStudentChanges(): void
    {
        $contribution = $this->contribution(Money::of(10, 'PLN'), students: 1);
        $extraStudent = new Student(new ClassRoom('4B'), 'Ewa', 'Nowak');

        $contribution->addStudent($extraStudent);
        self::assertTrue($contribution->getRemainingAmount()->isEqualTo(Money::of(20, 'PLN')));

        // Adding the same student again must not change the expected amount
        $contribution->addStudent($extraStudent);
        self::assertTrue($contribution->getRemainingAmount()->isEqualTo(Money::of(20, 'PLN')));

        $contribution->removeStudent($extraStudent);
        self::assertTrue($contribution->getRemainingAmount()->isEqualTo(Money::of(10, 'PLN')));
    }

    public function testProgressPercentageIsRatioOfPaidToExpectedAmount(): void
    {
        $contribution = $this->contribution(Money::of(10, 'PLN'), students: 4);
        $contribution->setTotalPaid(Money::of(10, 'PLN'));

        self::assertEqualsWithDelta(25.0, $contribution->getProgressPercentage(), 0.0001);
    }

    public function testProgressPercentageIsZeroWithoutStudentsOrWhenAmountIsZero(): void
    {
        self::assertSame(0.0, $this->contribution(Money::of(10, 'PLN'), students: 0)->getProgressPercentage());
        self::assertSame(0.0, $this->contribution(Money::of(0, 'PLN'), students: 2)->getProgressPercentage());
    }

    private function contribution(Money $amount, int $students): Contribution
    {
        $classRoom = new ClassRoom('4B');
        $contribution = new Contribution($classRoom, 'Składka', $amount);

        for ($i = 0; $i < $students; $i++) {
            $contribution->addStudent(new Student($classRoom, 'Uczeń' . $i, 'Testowy'));
        }

        return $contribution;
    }
}
